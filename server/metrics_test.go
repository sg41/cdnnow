package main

import (
	"testing"
	"time"
)

func TestRpsAddAndSnapshot(t *testing.T) {
	var tr rpsTracker
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 3; i++ {
		tr.add(base)
	}
	for i := 0; i < 2; i++ {
		tr.add(base.Add(time.Second))
	}

	var out [rpsWindow]int64
	tr.snapshot(base.Add(time.Second), out[:])

	if out[0] != 2 {
		t.Fatalf("age 0 = %d, want 2", out[0])
	}
	if out[1] != 3 {
		t.Fatalf("age 1 = %d, want 3", out[1])
	}
	for i := 2; i < rpsWindow; i++ {
		if out[i] != 0 {
			t.Fatalf("age %d = %d, want 0", i, out[i])
		}
	}
}

func TestRpsSlotReuseOnRollover(t *testing.T) {
	var tr rpsTracker
	base := time.Unix(1_700_000_000, 0)

	// rpsSlots == rpsWindow+1, so base and base+61s share a slot index.
	for i := 0; i < 5; i++ {
		tr.add(base)
	}
	for i := 0; i < 2; i++ {
		tr.add(base.Add(61 * time.Second))
	}

	var out [rpsWindow]int64
	tr.snapshot(base.Add(61*time.Second), out[:])

	if out[0] != 2 {
		t.Fatalf("age 0 = %d, want 2 (slot must reset, not accumulate)", out[0])
	}
}

func TestRpsEmptySnapshot(t *testing.T) {
	var tr rpsTracker
	var out [rpsWindow]int64
	tr.snapshot(time.Unix(1_700_000_000, 0), out[:])
	for i, v := range out {
		if v != 0 {
			t.Fatalf("age %d = %d, want 0", i, v)
		}
	}
}

func TestLatencyEmpty(t *testing.T) {
	var lt latTracker
	if got := lt.quantile(0.95); got != 0 {
		t.Fatalf("empty quantile = %v, want 0", got)
	}
}

func TestLatencyQuantiles(t *testing.T) {
	var lt latTracker
	for i := int64(1); i <= 100; i++ {
		lt.add(time.Duration(i)) // 1ns..100ns
	}
	// Sorted: buf[k] = (k+1)ns; k = ceil(p*n)-1.
	if got := lt.quantile(0.95); got != 95e-9 {
		t.Fatalf("p95 = %v, want 95ns", got)
	}
	if got := lt.quantile(0.99); got != 99e-9 {
		t.Fatalf("p99 = %v, want 99ns", got)
	}
	if got := lt.quantile(0.5); got != 50e-9 {
		t.Fatalf("p50 = %v, want 50ns", got)
	}
}

func TestLatencyRingCap(t *testing.T) {
	var lt latTracker
	const extra = 500
	for i := int64(1); i <= latCap+extra; i++ {
		lt.add(time.Duration(i))
	}
	if got := lt.total.Load(); got != latCap+extra {
		t.Fatalf("total = %d, want %d", got, latCap+extra)
	}
	// Ring holds the last latCap samples: 501ns..(latCap+500)ns.
	// p50 index: ceil(0.5*latCap)-1 = 8191 -> value 501+8191 = 8692ns.
	if got := lt.quantile(0.5); got != 8692e-9 {
		t.Fatalf("p50 over full ring = %v, want 8692ns", got)
	}
}
