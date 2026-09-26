// Package abi is a dynamic wrapper over the Needle C ABI (needle.h).
//
// The Needle engine ships as a self-contained shared library (libneedle.so on
// Linux, libneedle.dylib on macOS, needle.dll on Windows) whose only
// dependencies are libc/libm/libpthread. This package dlopen()s that library
// and resolves the needle_* symbols at runtime, so:
//
//   - neither the parent program nor the worker binary links the engine;
//   - the engine path is chosen at runtime (cache dir, ./engine, env var);
//   - the same Go binary works against the production engine or a test stub.
//
// The engine is process-global and non-thread-safe, so this package exposes a
// single Engine handle per process and the worker (internal/worker) runs one
// Engine per child process. See BINDINGS.md for the full story.
package abi

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// ErrNotOpen is returned when a method is called before Open.
var ErrNotOpen = errors.New("abi: engine not open")

// Engine is a handle to a loaded libneedle. Safe for use from one goroutine at
// a time; the worker guarantees that by construction.
type Engine struct {
	mu     sync.Mutex
	handle unsafe.Pointer

	loadFn    unsafe.Pointer
	initFn    unsafe.Pointer
	complFn   unsafe.Pointer
	embedFn   unsafe.Pointer
	resetFn   unsafe.Pointer
	lasterrFn unsafe.Pointer

	// dim caches the embedding dimension once queried.
	dim     int
	dimOnce bool
	dimErr  error
}

// Open loads the shared library at path and resolves every symbol the binding
// uses. It does not load a model; call Load next.
func Open(path string) (*Engine, error) {
	handle, err := dlopen(path)
	if err != nil {
		return nil, err
	}
	e := &Engine{handle: handle}
	syms := []struct {
		name string
		dst  *unsafe.Pointer
	}{
		{"needle_load", &e.loadFn},
		{"needle_init", &e.initFn},
		{"needle_complete", &e.complFn},
		{"needle_embed", &e.embedFn},
		{"needle_reset", &e.resetFn},
		{"needle_last_error", &e.lasterrFn},
	}
	for _, s := range syms {
		p, err := dlsym(handle, s.name)
		if err != nil {
			return nil, err
		}
		*s.dst = p
	}
	return e, nil
}

// Close releases the Go-side handle to the engine.
//
// It deliberately does NOT dlclose the library, and this is important.
//
// libneedle is a C++ shared object with static constructors, so it registers
// destructors via __cxa_atexit. dlclose unmaps the library, and the C++
// runtime then runs those destructors at process exit against unmapped
// memory — a segfault during exit, long after every test has passed. The
// header exposes no teardown entry point, so the engine is designed to stay
// mapped for the life of the process. This matches the reference Python
// binding, which loads with ctypes and never unloads.
//
// Unloading is also pointless in practice: the model is process-global, one
// per worker process, and the process exits right after.
//
// After Close, every method returns ErrNotOpen.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handle == nil {
		return nil
	}
	e.handle = nil
	for _, p := range []*unsafe.Pointer{&e.loadFn, &e.initFn, &e.complFn, &e.embedFn, &e.resetFn, &e.lasterrFn} {
		*p = nil
	}
	return nil
}

// Load loads a .cact archive (raw bytes) into the process-global model slot.
// Negative returns from the engine become a Go error quoting last_error.
func (e *Engine) Load(cact []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loadFn == nil {
		return ErrNotOpen
	}
	if len(cact) == 0 {
		return errors.New("abi: empty model bytes")
	}
	rc := callLoad(e.loadFn, cact)
	if rc < 0 {
		return fmt.Errorf("abi: needle_load: %s", e.lastErrorLocked())
	}
	return nil
}

// Init configures the static prefix and returns the tokenised prefix length.
func (e *Engine) Init(systemPrompt, toolsJSON, toolIndexPath string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.initFn == nil {
		return 0, ErrNotOpen
	}
	rc := callInit(e.initFn, systemPrompt, toolsJSON, toolIndexPath)
	if rc < 0 {
		return int(rc), fmt.Errorf("abi: needle_init: %s", e.lastErrorLocked())
	}
	return int(rc), nil
}

// Complete runs one generation step and returns the model's JSON reply.
//
// IMPORTANT: per the reference implementation, the only contract on the return
// value is "negative means failure". The result is a NUL-terminated C string
// in out, and its length is discovered by scanning for the terminator — the
// engine does not promise that a non-negative return equals the byte count.
// (Contrast Embed, where the return value IS the float count.) Reading off out
// until the NUL is therefore the safe mapping and matches needle/_worker.py.
func (e *Engine) Complete(input string, maxNewTokens int, out []byte) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.complFn == nil {
		return "", ErrNotOpen
	}
	if maxNewTokens <= 0 {
		return "", errors.New("abi: maxNewTokens must be positive")
	}
	if len(out) == 0 {
		return "", errors.New("abi: out buffer is empty")
	}
	// Zero the buffer so that an engine which forgets to terminate the
	// string cannot leak bytes from a previous call.
	clear(out)

	rc := callComplete(e.complFn, input, maxNewTokens, out)
	if rc < 0 {
		return "", fmt.Errorf("abi: needle_complete: %s", e.lastErrorLocked())
	}
	// The engine is required to write the terminator, but treat a missing
	// one as "output filled the buffer" rather than reading out of bounds.
	if i := bytes.IndexByte(out, 0); i >= 0 {
		return string(out[:i]), nil
	}
	return string(out), nil
}

// EmbedDim returns the embedding dimension, caching it after the first call.
func (e *Engine) EmbedDim() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dimOnce {
		return e.dim, e.dimErr
	}
	if e.embedFn == nil {
		return 0, ErrNotOpen
	}
	rc := callEmbedDim(e.embedFn)
	if rc <= 0 {
		e.dimOnce = true
		e.dimErr = fmt.Errorf("abi: needle_embed(dim): %s", e.lastErrorLocked())
		return 0, e.dimErr
	}
	e.dim = int(rc)
	e.dimOnce = true
	return e.dim, nil
}

// Embed fills out with the embedding vector and returns the number of floats
// written. Unlike Complete, the engine's return value here IS the count, so it
// is used directly (and cross-checked against the buffer length).
func (e *Engine) Embed(input string, out []float32) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.embedFn == nil {
		return 0, ErrNotOpen
	}
	if len(out) == 0 {
		return 0, errors.New("abi: out buffer is empty")
	}
	rc := callEmbed(e.embedFn, input, out)
	if rc < 0 {
		return 0, fmt.Errorf("abi: needle_embed: %s", e.lastErrorLocked())
	}
	if rc > len(out) {
		return int(rc), fmt.Errorf("abi: needle_embed reported %d floats into a %d-float buffer", rc, len(out))
	}
	return int(rc), nil
}

// Reset clears conversation history.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resetFn == nil {
		return
	}
	callReset(e.resetFn)
}

// LastError returns the most recent engine error message. The engine owns the
// pointer and invalidates it on the next ABI call, so this copies immediately.
func (e *Engine) LastError() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErrorLocked()
}

func (e *Engine) lastErrorLocked() string {
	if e.lasterrFn == nil {
		return "(no error hook)"
	}
	return callLastError(e.lasterrFn)
}
