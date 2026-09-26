// Child side of the worker protocol.
//
// The child reads one Config frame on stdin, opens the engine library, loads
// the model, initialises the static prefix, and then loops on Request frames
// until it sees op=="close" or stdin EOF.
//
// All error reporting goes back to the parent as a Response with status
// "error" or "fatal". Any panic is recovered and reported as a fatal frame so
// the parent surfaces a descriptive error instead of a bare exit code.
package worker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/mhs003/needless/needle/internal/abi"
)

// RunChild is the entry point for the child process. Its pipes are passed in
// so the function is testable without spawning a process.
func RunChild(stdin io.Reader, stdout io.Writer) int {
	w := stdout
	defer func() {
		if r := recover(); r != nil {
			_ = writeFrame(w, Response{
				Status:  "fatal",
				Message: fmt.Sprintf("panic: %v\n%s", r, debug.Stack()),
			})
		}
	}()

	var config Config
	if err := readFrame(stdin, &config); err != nil {
		_ = writeFrame(w, Response{Status: "fatal", Message: "config: " + err.Error()})
		return 2
	}
	if config.Library == "" {
		_ = writeFrame(w, Response{Status: "fatal", Message: "library path is required"})
		return 2
	}
	if config.Weights == "" {
		_ = writeFrame(w, Response{Status: "fatal", Message: "weights path is required"})
		return 2
	}
	if config.BufferSize <= 0 {
		_ = writeFrame(w, Response{Status: "fatal", Message: "buffer_size must be positive"})
		return 2
	}

	engine, err := abi.Open(config.Library)
	if err != nil {
		_ = writeFrame(w, Response{Status: "fatal", Message: err.Error()})
		return 3
	}
	defer func() { _ = engine.Close() }()

	weights, err := os.ReadFile(config.Weights)
	if err != nil {
		_ = writeFrame(w, Response{Status: "fatal", Message: "weights: " + err.Error()})
		return 4
	}
	if err := engine.Load(weights); err != nil {
		_ = writeFrame(w, Response{Status: "fatal", Message: err.Error()})
		return 5
	}
	prefix, err := engine.Init(config.System, config.Tools, config.ToolIndex)
	if err != nil {
		_ = writeFrame(w, Response{Status: "fatal", Message: err.Error()})
		return 6
	}

	out := make([]byte, config.BufferSize)
	var embedBuf []float32

	if err := writeFrame(w, Response{Status: "ready", PrefixTokens: prefix}); err != nil {
		return 7
	}

	for {
		var req Request
		if err := readFrame(stdin, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			_ = writeFrame(w, Response{Status: "fatal", Message: "request: " + err.Error()})
			return 8
		}

		switch req.Op {
		case "complete":
			body, err := engine.Complete(req.Text, req.MaxNew, out)
			if err != nil {
				_ = writeFrame(w, Response{Status: "error", Message: err.Error()})
				continue
			}
			_ = writeFrame(w, Response{Status: "ok", Text: body})
		case "embed":
			if req.Dim <= 0 {
				d, err := engine.EmbedDim()
				if err != nil {
					_ = writeFrame(w, Response{Status: "error", Message: err.Error()})
					continue
				}
				_ = writeFrame(w, Response{Status: "ok", Dim: d})
				continue
			}
			if cap(embedBuf) < req.Dim {
				embedBuf = make([]float32, req.Dim)
			} else {
				embedBuf = embedBuf[:req.Dim]
			}
			n, err := engine.Embed(req.Text, embedBuf)
			if err != nil {
				_ = writeFrame(w, Response{Status: "error", Message: err.Error()})
				continue
			}
			// Copy out: embedBuf is reused, so the frame must not alias it.
			vec := make([]float32, n)
			copy(vec, embedBuf)
			_ = writeFrame(w, Response{Status: "ok", Embedding: vec})
		case "reset":
			engine.Reset()
			_ = writeFrame(w, Response{Status: "ok"})
		case "close":
			_ = writeFrame(w, Response{Status: "ok"})
			return 0
		default:
			_ = writeFrame(w, Response{Status: "error", Message: fmt.Sprintf("unknown op %q", req.Op)})
		}
	}
}
