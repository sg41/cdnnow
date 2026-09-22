package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func resetStats() {
	okCount.Store(0)
	errCount.Store(0)
}

func runWorker(t *testing.T, handler http.HandlerFunc, interval time.Duration, runFor time.Duration) {
	t.Helper()
	resetStats()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(0, srv.URL, interval, srv.Client(), stop, &wg)
	time.Sleep(runFor)
	close(stop)
	wg.Wait()
}

func TestWorkerSuccess(t *testing.T) {
	runWorker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}, 0, 100*time.Millisecond)

	if got := okCount.Load(); got == 0 {
		t.Fatal("okCount = 0, want > 0")
	}
	if got := errCount.Load(); got != 0 {
		t.Fatalf("errCount = %d, want 0", got)
	}
}

func TestWorkerServerError(t *testing.T) {
	// Non-2xx responses count as errors; keep the run short and paced so
	// failure logging stays minimal.
	runWorker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, 10*time.Millisecond, 60*time.Millisecond)

	if got := okCount.Load(); got != 0 {
		t.Fatalf("okCount = %d, want 0", got)
	}
	if got := errCount.Load(); got == 0 {
		t.Fatal("errCount = 0, want > 0")
	}
}

func TestWorkerUnreachable(t *testing.T) {
	resetStats()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // further requests fail with connection refused

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	client := &http.Client{Timeout: 2 * time.Second}
	go worker(0, url, 10*time.Millisecond, client, stop, &wg)
	time.Sleep(60 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := errCount.Load(); got == 0 {
		t.Fatal("errCount = 0, want > 0 for unreachable server")
	}
	if got := okCount.Load(); got != 0 {
		t.Fatalf("okCount = %d, want 0", got)
	}
}
