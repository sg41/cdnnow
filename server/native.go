package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef int64_t (*binop_fn)(int64_t, int64_t);

static void* lib_open(const char* path) {
    return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}

static void* lib_sym(void* h, const char* name) {
    dlerror(); // clear
    void* s = dlsym(h, name);
    if (dlerror() != NULL) return NULL;
    return s;
}

static int64_t lib_call(void* fn, int64_t a, int64_t b) {
    return ((binop_fn)fn)(a, b);
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

// binop is one native accumulator function (C "add" or Rust "sub").
// The library is dlopen'ed at startup like Python's ctypes.CDLL; if the
// .so is missing, we fall back to a pure-Go reimplementation with
// identical semantics so the server still builds and runs anywhere.
type binop struct {
	name     string // "c" or "rust", for metrics labels
	fn       unsafe.Pointer
	fallback func(a, b int64) int64
	native   bool
}

func openBackend(name, libPath, symbol string, fallback func(a, b int64) int64) binop {
	b := binop{name: name, fallback: fallback}

	cpath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cpath))
	h := C.lib_open(cpath)
	if h == nil {
		fmt.Fprintf(os.Stderr, "warning: cannot load %s lib %q, using pure-Go fallback\n", name, libPath)
		return b
	}

	csym := C.CString(symbol)
	defer C.free(unsafe.Pointer(csym))
	fn := C.lib_sym(h, csym)
	if fn == nil {
		fmt.Fprintf(os.Stderr, "warning: symbol %q not found in %q, using pure-Go fallback\n", symbol, libPath)
		return b
	}

	// NOTE: handle intentionally kept open for process lifetime.
	b.fn = fn
	b.native = true
	return b
}

func (b *binop) call(a, c int64) int64 {
	if b.fn != nil {
		return int64(C.lib_call(b.fn, C.int64_t(a), C.int64_t(c)))
	}
	return b.fallback(a, c)
}

// goAdd / goSub mirror c_lib/calculator.c and rust_lib/src/lib.rs exactly
// (result plus a 10k xorshift busy loop), used when the .so is unavailable.
func goAdd(a, b int64) int64 {
	x := uint64(a + b)
	for i := 0; i < 10000; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
	}
	_ = x
	return a + b
}

func goSub(a, b int64) int64 {
	x := uint64(a - b)
	for i := 0; i < 10000; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
	}
	_ = x
	return a - b
}
