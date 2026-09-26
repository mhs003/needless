//go:build !unix

package abi

import (
	"errors"
	"unsafe"
)

// Dynamic loading is only implemented on unix. Windows would use LoadLibrary /
// GetProcAddress in a dlopen_windows.go; the trampolines in dlopen_unix.go map
// one-to-one onto that API. Until then, fail loudly rather than silently.
var errUnsupported = errors.New("abi: dynamic loading is not implemented on this platform")

func dlopen(string) (unsafe.Pointer, error)                { return nil, errUnsupported }
func dlsym(unsafe.Pointer, string) (unsafe.Pointer, error) { return nil, errUnsupported }

func callLoad(unsafe.Pointer, []byte) int                  { return -1 }
func callInit(unsafe.Pointer, string, string, string) int  { return -1 }
func callComplete(unsafe.Pointer, string, int, []byte) int { return -1 }
func callEmbedDim(unsafe.Pointer) int                      { return -1 }
func callEmbed(unsafe.Pointer, string, []float32) int      { return -1 }
func callReset(unsafe.Pointer)                             {}
func callLastError(unsafe.Pointer) string                  { return "" }
