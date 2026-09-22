// Package main implements the calculator HTTP server.
//
// It mirrors calculator_server.py: POST /calc?num=X accumulates
// sum via the C "add" function and sub via the Rust "sub" function,
// plus a Prometheus /metrics endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	sumValue   atomic.Int64
	subValue   atomic.Int64
	okRequests atomic.Uint64

	rpsTrackerInst rpsTracker
	latC           latTracker
	latRust        latTracker

	cBackend    binop
	rustBackend binop
)

var okBody = []byte("ok")

// resolveLibPath maps a (possibly relative) library path to an existing
// file. Relative paths are tried against the current working directory
// first, then against the server binary's own directory — so
// bin/calculator_server finds bin/*.so no matter where it is launched
// from. Absolute paths pass through untouched.
func resolveLibPath(p string) string {
	exe, err := os.Executable()
	if err != nil {
		return p
	}
	return resolveLibPathAt(p, filepath.Dir(exe))
}

func resolveLibPathAt(p, exeDir string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if cand := filepath.Join(exeDir, p); cand != p {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return p // let dlopen report it; backend falls back to pure Go
}

func main() {
	var (
		host     = flag.String("host", "0.0.0.0", "address to listen on")
		port     = flag.Int("port", 8080, "port to listen on")
		cLib     = flag.String("c-lib", "libcalculator.so", "path to the C shared library")
		rustLib  = flag.String("rust-lib", "libcalculator_rust.so", "path to the Rust shared library")
		interval = flag.Float64("interval", 5.0, "seconds between periodic sum/sub reports (<=0 disables)")
	)
	flag.Parse()

	cBackend = openBackend("c", resolveLibPath(*cLib), "add", goAdd)
	rustBackend = openBackend("rust", resolveLibPath(*rustLib), "sub", goSub)

	mux := http.NewServeMux()
	mux.HandleFunc("/calc", calcHandler)
	mux.HandleFunc("/metrics", metricsHandler)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No read/write timeouts on the hot path: the handler is
		// CPU-bound and tiny, timeouts would only add timer overhead.
		MaxHeaderBytes: 1 << 13, // 8 KiB, query is a few bytes
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *host, *port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Calculator server listening on %s\n", ln.Addr().String())

	// Periodic sum/sub reporter (parity with calculator_server.py).
	var ticker *time.Ticker
	var tickerCh <-chan time.Time
	if *interval > 0 {
		ticker = time.NewTicker(time.Duration(*interval * float64(time.Second)))
		tickerCh = ticker.C
		go func() {
			for range tickerCh {
				printTotals("periodic")
			}
		}()
	}

	// Graceful shutdown on SIGINT/SIGTERM (parity with Python handler).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nSIGINT received, shutting down...")
		printTotals("final")
		if ticker != nil {
			ticker.Stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func printTotals(label string) {
	fmt.Printf("[%s] sum=%d sub=%d\n", label, sumValue.Load(), subValue.Load())
}

// calcHandler serves POST /calc?num=X.
//
// Fast-path design:
//   - no body read (num comes from the query, same as Python);
//   - lock-free state: add/sub are commutative, so plain atomic adds
//     preserve the exact final sums without serializing requests;
//   - native calls run sequentially in the request goroutine: spawning
//     extra goroutines per request would add overhead without raising
//     total throughput (same CPU work, more scheduling).
func calcHandler(w http.ResponseWriter, r *http.Request) {
	// Count every hit of the /calc route (any method, any result).
	rpsTrackerInst.add(time.Now())

	if r.URL.Path != "/calc" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	num, code := parseNumParam(r.URL.RawQuery)
	switch code {
	case numMissing:
		http.Error(w, "missing 'num' query parameter", http.StatusBadRequest)
		return
	case numBad:
		http.Error(w, "'num' must be an integer", http.StatusBadRequest)
		return
	}

	// Snapshot the accumulators as native call arguments so the timed
	// call observes realistic inputs; the authoritative update is the
	// atomic add below, which keeps the exact final sums under concurrency.
	t0 := time.Now()
	cBackend.call(sumValue.Load(), num)
	dC := time.Since(t0)

	t1 := time.Now()
	rustBackend.call(subValue.Load(), num)
	dR := time.Since(t1)

	sumValue.Add(num)
	subValue.Add(-num)

	latC.add(dC)
	latRust.add(dR)
	okRequests.Add(1)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(okBody)
}
