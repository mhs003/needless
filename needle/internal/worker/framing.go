// Package worker contains the framed protocol spoken between the parent
// process (the Go program that imported the needle package) and the child process
// that hosts libneedle.
//
// Why a process boundary? The Needle engine is process-global and
// non-thread-safe: it owns one model and one conversation, and concurrent
// calls would corrupt state. One worker per Needle value lets a Go program
// hold several models side-by-side with no locking in user code — the same
// design the reference Python implementation uses in needle/_worker.py.
//
// Framing: a big-endian uint64 length prefix followed by a UTF-8 JSON payload,
// on both stdin (parent -> child) and stdout (child -> parent). This matches
// the Python protocol byte for byte.
package worker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// Config is the first frame the parent sends. It is the only frame that
// carries load-time settings.
type Config struct {
	// Library is the path to the engine shared library (libneedle.so,
	// libneedle.dylib, needle.dll).
	Library string `json:"library"`
	// Weights is the path to the .cact model archive.
	Weights string `json:"weights"`
	// System is the static system prompt baked into the prefix.
	System string `json:"system"`
	// Tools is the JSON array of tool schemas.
	Tools string `json:"tools"`
	// ToolIndex, if set, is a path to a saved retrieval index.
	ToolIndex string `json:"tool_index,omitempty"`
	// BufferSize is the capacity in bytes of the completion buffer.
	BufferSize int `json:"buffer_size"`
}

// Request is a single operation sent to the child.
type Request struct {
	Op       string `json:"op"` // complete | embed | reset | close
	Text     string `json:"text,omitempty"`
	MaxNew   int    `json:"max_new_tokens,omitempty"`
	Dim      int    `json:"dim,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// Response is the child's answer to a Request (or the result of startup).
type Response struct {
	Status       string    `json:"status"` // ready | ok | error | fatal
	Message      string    `json:"message,omitempty"`
	Text         string    `json:"text,omitempty"`
	Embedding    []float32 `json:"embedding,omitempty"`
	Dim          int       `json:"dim,omitempty"`
	PrefixTokens int       `json:"prefix_tokens,omitempty"`
}

// writeFrame marshals v and writes it with a length prefix.
func writeFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// readFrame reads one length-prefixed JSON frame into v.
//
// io.EOF on the header means the peer closed cleanly. A truncated payload (or
// a length so large it cannot be real) is reported as an error so the caller
// can tell "graceful shutdown" from "process died mid-write".
func readFrame(r io.Reader, v any) error {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return err
	}
	n := binary.BigEndian.Uint64(hdr[:])
	if n == 0 {
		// A zero-length payload is a protocol violation, but treat it as
		// EOF to stay compatible with a child that exits abruptly.
		return io.EOF
	}
	if n > 1<<30 {
		return errors.New("worker: frame exceeds 1 GiB limit")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return errors.New("worker: truncated frame payload")
	}
	return json.Unmarshal(buf, v)
}
