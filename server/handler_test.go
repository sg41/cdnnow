package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetState clears all global server state and forces the pure-Go
// backends, so tests are deterministic and independent of .so files.
func resetState() {
	sumValue.Store(0)
	subValue.Store(0)
	okRequests.Store(0)
	rpsTrackerInst = rpsTracker{}
	latC = latTracker{}
	latRust = latTracker{}
	cBackend = binop{name: "c", fallback: goAdd}
	rustBackend = binop{name: "rust", fallback: goSub}
}

func TestGoFallbackArithmetic(t *testing.T) {
	if got := goAdd(2, 3); got != 5 {
		t.Fatalf("goAdd(2,3) = %d, want 5", got)
	}
	if got := goAdd(-10, 4); got != -6 {
		t.Fatalf("goAdd(-10,4) = %d, want -6", got)
	}
	if got := goSub(2, 3); got != -1 {
		t.Fatalf("goSub(2,3) = %d, want -1", got)
	}
	if got := goSub(-10, 4); got != -14 {
		t.Fatalf("goSub(-10,4) = %d, want -14", got)
	}
}

func TestOpenBackendFallback(t *testing.T) {
	b := openBackend("c", "/nonexistent-path-xyz.so", "add", goAdd)
	if b.native {
		t.Fatal("expected fallback for missing library")
	}
	if got := b.call(2, 3); got != 5 {
		t.Fatalf("fallback call(2,3) = %d, want 5", got)
	}
}

func TestResolveLibPath(t *testing.T) {
	t.Run("absolute passthrough", func(t *testing.T) {
		if got := resolveLibPathAt("/opt/libs/libcalculator.so", t.TempDir()); got != "/opt/libs/libcalculator.so" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("missing stays as is", func(t *testing.T) {
		if got := resolveLibPathAt("no-such-lib-xyz.so", t.TempDir()); got != "no-such-lib-xyz.so" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("found next to binary", func(t *testing.T) {
		exeDir := t.TempDir()
		name := "testlib-next-to-bin.so"
		if err := os.WriteFile(filepath.Join(exeDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := resolveLibPathAt(name, exeDir); got != filepath.Join(exeDir, name) {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("cwd wins over binary dir", func(t *testing.T) {
		name := "testlib-cwd-wins.so"
		cwdDir := t.TempDir()
		exeDir := t.TempDir()
		for _, d := range []string{cwdDir, exeDir} {
			if err := os.WriteFile(filepath.Join(d, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(cwdDir); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(oldWd); err != nil {
				t.Fatal(err)
			}
		}()
		if got := resolveLibPathAt(name, exeDir); got != name {
			t.Fatalf("got %q, want CWD-relative %q", got, name)
		}
	})
}

func TestCalcHandlerSuccess(t *testing.T) {
	resetState()

	req := httptest.NewRequest(http.MethodPost, "/calc?num=7", nil)
	rec := httptest.NewRecorder()
	calcHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok")
	}
	if got := sumValue.Load(); got != 7 {
		t.Fatalf("sum = %d, want 7", got)
	}
	if got := subValue.Load(); got != -7 {
		t.Fatalf("sub = %d, want -7", got)
	}
	if got := okRequests.Load(); got != 1 {
		t.Fatalf("okRequests = %d, want 1", got)
	}
	if got := latC.total.Load(); got != 1 {
		t.Fatalf("c calls = %d, want 1", got)
	}
	if got := latRust.total.Load(); got != 1 {
		t.Fatalf("rust calls = %d, want 1", got)
	}
}

func TestCalcHandlerErrors(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		target   string
		wantCode int
		wantBody string
	}{
		{"missing num", http.MethodPost, "/calc", 400, "missing 'num' query parameter"},
		{"blank num", http.MethodPost, "/calc?num=", 400, "missing 'num' query parameter"},
		{"bad num", http.MethodPost, "/calc?num=abc", 400, "'num' must be an integer"},
		{"wrong method", http.MethodGet, "/calc?num=1", 405, "method not allowed"},
		{"wrong path", http.MethodPost, "/nope?num=1", 404, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetState()
			req := httptest.NewRequest(tt.method, tt.target, nil)
			rec := httptest.NewRecorder()
			calcHandler(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if got := okRequests.Load(); got != 0 {
				t.Fatalf("okRequests = %d, want 0", got)
			}
			if got := sumValue.Load(); got != 0 || subValue.Load() != 0 {
				t.Fatalf("state changed on error: sum=%d sub=%d", sumValue.Load(), subValue.Load())
			}
		})
	}
}

func TestCalcHandlerConcurrentInvariant(t *testing.T) {
	resetState()

	// sum += num / sub -= num per request, so sum+sub must stay 0
	// under any concurrency level.
	const workers = 8
	const perWorker = 250
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				num := (w*perWorker+i)%201 - 100
				req := httptest.NewRequest(http.MethodPost, "/calc?num="+strconv.Itoa(num), nil)
				rec := httptest.NewRecorder()
				calcHandler(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("code = %d, want 200", rec.Code)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if got := sumValue.Load() + subValue.Load(); got != 0 {
		t.Fatalf("sum+sub = %d, want 0 (sum=%d sub=%d)",
			got, sumValue.Load(), subValue.Load())
	}
	if got := okRequests.Load(); got != workers*perWorker {
		t.Fatalf("okRequests = %d, want %d", got, workers*perWorker)
	}
}

func TestMetricsHandlerFormat(t *testing.T) {
	resetState()

	// Seed deterministic state.
	now := time.Now()
	for i := 0; i < 3; i++ {
		rpsTrackerInst.add(now)
	}
	okRequests.Store(42)
	latC.add(1000 * time.Nanosecond)
	rustBackend.call(0, 0) // exercise fallback path
	latRust.add(2000 * time.Nanosecond)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	metricsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("Content-Type = %q, want Prometheus exposition format", ct)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"# HELP calculator_http_rps",
		"# TYPE calculator_http_rps gauge",
		"calculator_http_requests_total 42",
		"calculator_c_call_duration_seconds{quantile=\"0.95\"}",
		"calculator_c_call_duration_seconds{quantile=\"0.99\"}",
		"calculator_c_calls_total 1",
		"calculator_rust_call_duration_seconds{quantile=\"0.95\"}",
		"calculator_rust_call_duration_seconds{quantile=\"0.99\"}",
		"calculator_rust_calls_total 1",
		"calculator_backend_info{backend=\"c\",impl=\"go_fallback\"} 1",
		"calculator_backend_info{backend=\"rust\",impl=\"go_fallback\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q\nfull body:\n%s", want, body)
		}
	}

	// Exactly 60 per-second rps lines whose values sum to the 3 seeded hits
	// (robust to a second boundary between seeding and scrape).
	var lines, sum int64
	for _, line := range strings.Split(body, "\n") {
		var age int
		var v int64
		if n, _ := fmt.Sscanf(line, "calculator_http_rps{age_seconds=\"%d\"} %d", &age, &v); n == 2 {
			lines++
			sum += v
		}
	}
	if lines != rpsWindow {
		t.Fatalf("rps lines = %d, want %d", lines, rpsWindow)
	}
	if sum != 3 {
		t.Fatalf("rps total = %d, want 3", sum)
	}
}
