// Command generator is a load generator for the calculator server.
//
// It mirrors generator.py: N workers POST random num in [-100, 100] to
// /calc in a loop, with a shared keep-alive HTTP client.
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	okCount  atomic.Uint64
	errCount atomic.Uint64
)

func worker(id int, baseURL string, interval time.Duration, client *http.Client, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*1000003))
	prefix := baseURL + "?num="
	for {
		select {
		case <-stop:
			return
		default:
		}

		num := rng.Intn(201) - 100
		req, err := http.NewRequest(http.MethodPost, prefix+strconv.Itoa(num), nil)
		if err != nil {
			errCount.Add(1)
			fmt.Printf("[worker %d] request failed: %v\n", id, err)
		} else {
			resp, err := client.Do(req)
			if err != nil {
				errCount.Add(1)
				fmt.Printf("[worker %d] request failed: %v\n", id, err)
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					okCount.Add(1)
				} else {
					errCount.Add(1)
					fmt.Printf("[worker %d] request failed: unexpected status %s\n", id, resp.Status)
				}
			}
		}

		if interval > 0 {
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
		}
	}
}

func main() {
	var (
		url      = flag.String("url", "http://localhost:8080/calc", "calculator endpoint")
		threads  = flag.Int("threads", 10, "number of worker goroutines")
		interval = flag.Float64("interval", 0.1, "pause between requests per worker, in seconds (0 = as fast as possible)")
		timeout  = flag.Float64("timeout", 5.0, "HTTP request timeout, seconds")
	)
	// Alias -n for --threads (parity with argparse "-n/--threads").
	flag.IntVar(threads, "n", 10, "alias for --threads")
	flag.Parse()

	if *threads <= 0 {
		fmt.Fprintln(os.Stderr, "threads must be > 0")
		os.Exit(1)
	}

	client := &http.Client{
		Timeout: time.Duration(*timeout * float64(time.Second)),
		Transport: &http.Transport{
			MaxIdleConns:        *threads * 2,
			MaxIdleConnsPerHost: *threads,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // tiny bodies, skip gzip negotiation
		},
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < *threads; i++ {
		wg.Add(1)
		go worker(i, *url, time.Duration(*interval*float64(time.Second)), client, stop, &wg)
	}

	fmt.Printf("Generator started: %d threads -> %s\n", *threads, *url)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\nSIGINT received, stopping generator...")
	close(stop)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	fmt.Printf("Total requests: ok=%d errors=%d\n", okCount.Load(), errCount.Load())
}
