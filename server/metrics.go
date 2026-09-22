package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// RPS: per-second counters for the last 60 seconds.
// ---------------------------------------------------------------------------

const (
	rpsWindow = 60
	rpsSlots  = rpsWindow + 1 // +1 so "now" and "now-60" never share a slot
)

type rpsTracker struct {
	secs   [rpsSlots]atomic.Int64
	counts [rpsSlots]atomic.Int64
}

// add records one request in the current second's slot. Lock-free:
// the common case is a single atomic add; only on second rollover does
// a CAS reset the slot (loser retries and then adds).
func (t *rpsTracker) add(now time.Time) {
	sec := now.Unix()
	idx := sec % rpsSlots
	for {
		s := t.secs[idx].Load()
		if s == sec {
			t.counts[idx].Add(1)
			return
		}
		if t.secs[idx].CompareAndSwap(s, sec) {
			t.counts[idx].Store(1)
			return
		}
	}
}

// snapshot fills out[0..59] with counts for now, now-1s, ..., now-59s.
func (t *rpsTracker) snapshot(now time.Time, out []int64) {
	sec := now.Unix()
	for i := 0; i < rpsWindow; i++ {
		s := sec - int64(i)
		idx := s % rpsSlots
		if t.secs[idx].Load() == s {
			out[i] = t.counts[idx].Load()
		} else {
			out[i] = 0
		}
	}
}

// ---------------------------------------------------------------------------
// Latency: sliding window of the last N call durations (lock-free ring).
// ---------------------------------------------------------------------------

const latCap = 16384 // samples per backend; scrape sorts a copy

type latTracker struct {
	total atomic.Uint64 // number of samples ever recorded
	slots [latCap]atomic.Uint64
}

func (t *latTracker) add(d time.Duration) {
	n := uint64(d.Nanoseconds())
	if n == 0 {
		n = 1
	}
	i := t.total.Add(1) - 1
	t.slots[i%latCap].Store(n)
}

func (t *latTracker) quantile(p float64) float64 {
	total := t.total.Load()
	n := total
	if n > latCap {
		n = latCap
	}
	if n == 0 {
		return 0
	}
	buf := make([]uint64, n)
	for i := uint64(0); i < n; i++ {
		buf[i] = t.slots[i].Load()
	}
	sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
	k := int(math.Ceil(p*float64(n))) - 1
	if k < 0 {
		k = 0
	}
	if k >= len(buf) {
		k = len(buf) - 1
	}
	return float64(buf[k]) / 1e9
}

// ---------------------------------------------------------------------------
// Prometheus exposition.
// ---------------------------------------------------------------------------

func backendImpl(b *binop) string {
	if b.native {
		return "native"
	}
	return "go_fallback"
}

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	var rps [rpsWindow]int64
	rpsTrackerInst.snapshot(now, rps[:])

	cP95 := latC.quantile(0.95)
	cP99 := latC.quantile(0.99)
	rP95 := latRust.quantile(0.95)
	rP99 := latRust.quantile(0.99)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprintln(w, "# HELP calculator_http_rps Number of /calc requests (any method/result) in each of the last 60 seconds (age 0 = current partial second).")
	fmt.Fprintln(w, "# TYPE calculator_http_rps gauge")
	for i := 0; i < rpsWindow; i++ {
		fmt.Fprintf(w, "calculator_http_rps{age_seconds=\"%d\"} %d\n", i, rps[i])
	}

	fmt.Fprintln(w, "# HELP calculator_http_requests_total Total successful POST /calc requests.")
	fmt.Fprintln(w, "# TYPE calculator_http_requests_total counter")
	fmt.Fprintf(w, "calculator_http_requests_total %d\n", okRequests.Load())

	fmt.Fprintln(w, "# HELP calculator_c_call_duration_seconds p95/p99 of the C add() call duration over the last 16384 calls, in seconds.")
	fmt.Fprintln(w, "# TYPE calculator_c_call_duration_seconds gauge")
	fmt.Fprintf(w, "calculator_c_call_duration_seconds{quantile=\"0.95\"} %g\n", cP95)
	fmt.Fprintf(w, "calculator_c_call_duration_seconds{quantile=\"0.99\"} %g\n", cP99)
	fmt.Fprintln(w, "# HELP calculator_c_calls_total Total C add() calls measured.")
	fmt.Fprintln(w, "# TYPE calculator_c_calls_total counter")
	fmt.Fprintf(w, "calculator_c_calls_total %d\n", latC.total.Load())

	fmt.Fprintln(w, "# HELP calculator_rust_call_duration_seconds p95/p99 of the Rust sub() call duration over the last 16384 calls, in seconds.")
	fmt.Fprintln(w, "# TYPE calculator_rust_call_duration_seconds gauge")
	fmt.Fprintf(w, "calculator_rust_call_duration_seconds{quantile=\"0.95\"} %g\n", rP95)
	fmt.Fprintf(w, "calculator_rust_call_duration_seconds{quantile=\"0.99\"} %g\n", rP99)
	fmt.Fprintln(w, "# HELP calculator_rust_calls_total Total Rust sub() calls measured.")
	fmt.Fprintln(w, "# TYPE calculator_rust_calls_total counter")
	fmt.Fprintf(w, "calculator_rust_calls_total %d\n", latRust.total.Load())

	fmt.Fprintln(w, "# HELP calculator_backend_info Backend implementation in use: native (.so) or go_fallback.")
	fmt.Fprintln(w, "# TYPE calculator_backend_info gauge")
	fmt.Fprintf(w, "calculator_backend_info{backend=\"c\",impl=\"%s\"} 1\n", backendImpl(&cBackend))
	fmt.Fprintf(w, "calculator_backend_info{backend=\"rust\",impl=\"%s\"} 1\n", backendImpl(&rustBackend))
}
