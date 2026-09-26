//go:build unix

package abi

/*
#cgo CFLAGS: -I${SRCDIR}/../../engine
#cgo linux LDFLAGS: -ldl
#cgo darwin LDFLAGS: -ldl

#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

#include "needle.h"

// The engine ships as a shared library, so we resolve its functions at runtime
// with dlsym. cgo cannot call a C function pointer directly, so we cast the
// resolved pointer back to its real prototype inside these trampolines. The
// prototypes are checked against needle.h at compile time.

typedef int  (*load_fn)(const unsigned char *, unsigned long long);
typedef int  (*init_fn)(const char *, const char *, const char *);
typedef int  (*complete_fn)(const char *, int, char *, int);
typedef int  (*embed_fn)(const char *, float *, int);
typedef void (*reset_fn)(void);
typedef const char *(*lasterr_fn)(void);

static void *n_dlopen(const char *path) {
	return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}
static void *n_dlsym(void *handle, const char *name) {
	return dlsym(handle, name);
}
static const char *n_dlerror(void) {
	return dlerror();
}

static int call_load(void *fn, const unsigned char *p, unsigned long long n) {
	return ((load_fn)fn)(p, n);
}
static int call_init(void *fn, const char *a, const char *b, const char *c) {
	return ((init_fn)fn)(a, b, c);
}
static int call_complete(void *fn, const char *in, int max, char *out, int cap) {
	return ((complete_fn)fn)(in, max, out, cap);
}
static int call_embed(void *fn, const char *in, float *out, int cap) {
	return ((embed_fn)fn)(in, out, cap);
}
static void call_reset(void *fn) {
	((reset_fn)fn)();
}
static const char *call_lasterr(void *fn) {
	return ((lasterr_fn)fn)();
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func dlopen(path string) (unsafe.Pointer, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	h := C.n_dlopen(cpath)
	if h == nil {
		msg := C.n_dlerror()
		if msg == nil {
			return nil, fmt.Errorf("dlopen %s: unknown error", path)
		}
		return nil, fmt.Errorf("dlopen %s: %s", path, C.GoString(msg))
	}
	return h, nil
}

func dlsym(handle unsafe.Pointer, name string) (unsafe.Pointer, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	p := C.n_dlsym(handle, cname)
	if p == nil {
		return nil, fmt.Errorf("dlsym %s: symbol not found", name)
	}
	return p, nil
}

func callLoad(fn unsafe.Pointer, cact []byte) int {
	var p *C.uchar
	if len(cact) > 0 {
		p = (*C.uchar)(unsafe.Pointer(&cact[0]))
	}
	return int(C.call_load(fn, p, C.ulonglong(len(cact))))
}

func callInit(fn unsafe.Pointer, system, tools, index string) int {
	csystem := cstr(system)
	ctools := cstr(tools)
	cindex := cstr(index)
	defer freeIf(csystem)
	defer freeIf(ctools)
	defer freeIf(cindex)
	return int(C.call_init(fn, csystem, ctools, cindex))
}

func callComplete(fn unsafe.Pointer, input string, maxNew int, out []byte) int {
	cin := cstr(input)
	defer freeIf(cin)
	return int(C.call_complete(fn, cin, C.int(maxNew), (*C.char)(unsafe.Pointer(&out[0])), C.int(len(out))))
}

func callEmbedDim(fn unsafe.Pointer) int {
	return int(C.call_embed(fn, nil, nil, 0))
}

func callEmbed(fn unsafe.Pointer, input string, out []float32) int {
	cin := cstr(input)
	defer freeIf(cin)
	return int(C.call_embed(fn, cin, (*C.float)(unsafe.Pointer(&out[0])), C.int(len(out))))
}

func callReset(fn unsafe.Pointer) {
	C.call_reset(fn)
}

func callLastError(fn unsafe.Pointer) string {
	p := C.call_lasterr(fn)
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func cstr(s string) *C.char {
	if s == "" {
		return nil
	}
	return C.CString(s)
}

func freeIf(p *C.char) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}
