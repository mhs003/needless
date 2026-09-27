// Parent side of the worker protocol. One Worker == one child process that
// owns one model and one conversation.
//
// # Concurrency contract
//
// The engine is process-global and non-thread-safe, so every ABI call must be
// serialised. ioMu is held across the *whole* write-then-read cycle, which is
// what keeps responses paired with their requests: guarding only the write
// would let two goroutines interleave and steal each other's replies.
//
// Close and kill never take ioMu, so a call blocked on a read can always be
// unblocked by killing the child. That is what makes context cancellation work.
package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errClosed = errors.New("worker: closed")

// Worker is the parent-side handle to a child process. Always construct with
// Start; the zero value is not usable.
type Worker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// ioMu serialises the write-then-read cycle. readErr is guarded by it.
	ioMu    sync.Mutex
	readErr error

	closed       atomic.Bool
	prefixTokens int

	doneOnce sync.Once
	done     chan struct{}
}

// PrefixTokens reports the tokenised length of the static prefix returned by
// needle_init. Immutable after Start.
func (w *Worker) PrefixTokens() int { return w.prefixTokens }

// Closed reports whether Close has been called.
func (w *Worker) Closed() bool { return w.closed.Load() }

// Done returns a channel closed when the child process exits.
func (w *Worker) Done() <-chan struct{} { return w.done }

// PID returns the child's OS process id, or -1 if it has exited.
func (w *Worker) PID() int {
	if w.cmd == nil || w.cmd.Process == nil {
		return -1
	}
	return w.cmd.Process.Pid
}

// telemetryEnv are the variables the upstream engine reads to disable its own
// usage reporting.
var telemetryEnv = []string{"NEEDLE_TELEMETRY=0", "DO_NOT_TRACK=1"}

// workerEnv returns the child's environment with the engine's telemetry
// disabled.
//
// Needless is a local, on-device tool, and a component that reports usage by
// default contradicts what the program is for. The opt-out is applied here
// because Start is the one choke point every caller passes through, and it is
// applied *only when the variable is not already set*, so a user who exports
// NEEDLE_TELEMETRY=1 still opts back in (D52).
//
// It is a pure function of its input so the policy is testable without
// spawning a process.
func workerEnv(base []string) []string {
	out := make([]string, 0, len(base)+len(telemetryEnv))
	out = append(out, base...)

	for _, kv := range telemetryEnv {
		name, _, _ := strings.Cut(kv, "=")
		if !envHas(base, name) {
			out = append(out, kv)
		}
	}
	return out
}

// envHas reports whether base already defines name. An empty value counts as
// defined: `NEEDLE_TELEMETRY=` is a choice, not an omission, and overriding it
// would be ignoring what the user said.
func envHas(base []string, name string) bool {
	prefix := name + "="
	for _, kv := range base {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// Start spawns the worker child, sends the config, and waits for the "ready"
// frame. workerPath overrides discovery of the child binary (tests use it).
//
// Discovery order when workerPath is empty:
//  1. $NEEDLE_WORKER
//  2. needle-worker beside the current executable
//  3. needle-worker on $PATH
func Start(ctx context.Context, workerPath string, cfg Config) (*Worker, error) {
	if cfg.BufferSize == 0 {
		cfg.BufferSize = 64 * 1024
	}
	if cfg.Library == "" {
		return nil, errors.New("worker: library path is required")
	}
	if cfg.Weights == "" {
		return nil, errors.New("worker: weights path is required")
	}
	if cfg.BufferSize < 0 {
		return nil, errors.New("worker: buffer_size must be positive")
	}

	path, err := resolveWorkerPath(workerPath)
	if err != nil {
		return nil, err
	}

	// Deliberately NOT exec.CommandContext: the child outlives the context
	// passed to Start, which callers often scope to startup. ctx is honoured
	// only for the initial handshake below.
	cmd := exec.Command(path, "--child")
	cmd.Stderr = os.Stderr
	// Inherited, minus the engine's telemetry (D52).
	cmd.Env = workerEnv(os.Environ())

	// The pipes are created explicitly rather than with StdinPipe/StdoutPipe.
	// Wait() closes the pipes those helpers hand back, and it runs in the
	// goroutine below from the moment the child starts — so a reader could
	// find its end closed mid-frame ("file already closed") whenever the child
	// exited quickly, which a startup failure always does. Pipes we own and
	// pass as *os.File are used directly by the child and are not touched by
	// Wait(), so the reader sees the child's final frame and then a clean EOF.
	cliStdin, ourStdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("worker: stdin pipe: %w", err)
	}
	ourStdout, cliStdout, err := os.Pipe()
	if err != nil {
		_ = cliStdin.Close()
		_ = ourStdin.Close()
		return nil, fmt.Errorf("worker: stdout pipe: %w", err)
	}
	cmd.Stdin = cliStdin
	cmd.Stdout = cliStdout

	if err := cmd.Start(); err != nil {
		_ = cliStdin.Close()
		_ = ourStdin.Close()
		_ = ourStdout.Close()
		_ = cliStdout.Close()
		return nil, fmt.Errorf("worker: start: %w", err)
	}

	// The parent must not keep the child's ends open, or it would never see
	// EOF: the child's exit is only visible once every copy of the write end
	// is closed.
	_ = cliStdin.Close()
	_ = cliStdout.Close()

	w := &Worker{
		cmd:    cmd,
		stdin:  ourStdin,
		stdout: bufio.NewReader(ourStdout),
		done:   make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		w.doneOnce.Do(func() { close(w.done) })
	}()

	if err := writeFrame(ourStdin, cfg); err != nil {
		_ = w.kill()
		return nil, fmt.Errorf("worker: send config: %w", err)
	}

	// Startup covers dlopen plus reading and mapping the .cact weights, which
	// takes a moment for the 34 MiB archive. Race it against ctx so a caller
	// can give up on a slow or wedged engine.
	type startupResult struct {
		resp Response
		err  error
	}
	startupCh := make(chan startupResult, 1)
	go func() {
		resp, err := w.readResponse(2 * time.Minute)
		startupCh <- startupResult{resp, err}
	}()

	var resp Response
	select {
	case <-ctx.Done():
		_ = w.kill()
		return nil, ctx.Err()
	case r := <-startupCh:
		if r.err != nil {
			_ = w.kill()
			return nil, fmt.Errorf("worker: startup: %w", r.err)
		}
		resp = r.resp
	}
	if resp.Status != "ready" {
		_ = w.kill()
		return nil, fmt.Errorf("worker: startup failed: %s", resp.Message)
	}
	w.prefixTokens = resp.PrefixTokens
	return w, nil
}

// Complete runs a generation request and returns the model's JSON reply.
func (w *Worker) Complete(ctx context.Context, text string, maxNewTokens int) (string, error) {
	resp, err := w.call(ctx, Request{Op: "complete", Text: text, MaxNew: maxNewTokens})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// EmbedDim returns the model's embedding dimension.
func (w *Worker) EmbedDim(ctx context.Context) (int, error) {
	resp, err := w.call(ctx, Request{Op: "embed"})
	if err != nil {
		return 0, err
	}
	return resp.Dim, nil
}

// Embed returns the embedding vector for text.
func (w *Worker) Embed(ctx context.Context, text string) ([]float32, error) {
	dim, err := w.EmbedDim(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := w.call(ctx, Request{Op: "embed", Text: text, Dim: dim})
	if err != nil {
		return nil, err
	}
	return resp.Embedding, nil
}

// Reset clears the conversation history.
func (w *Worker) Reset(ctx context.Context) error {
	_, err := w.call(ctx, Request{Op: "reset"})
	return err
}

// Close shuts the child down. It first tries the graceful close handshake if
// no call is in flight, then falls back to killing the process. Idempotent.
func (w *Worker) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	// Graceful path only when the worker is idle. TryLock never blocks, so a
	// call stuck on a read cannot deadlock shutdown.
	if w.ioMu.TryLock() {
		_ = writeFrame(w.stdin, Request{Op: "close"})
		_ = w.stdin.Close()
		w.ioMu.Unlock()
		select {
		case <-w.done:
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return w.kill()
}

func (w *Worker) call(ctx context.Context, req Request) (Response, error) {
	if w.closed.Load() {
		return Response{}, errClosed
	}

	type result struct {
		resp Response
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		w.ioMu.Lock()
		defer w.ioMu.Unlock()
		// Re-check under the lock: Close may have won the race.
		if w.closed.Load() {
			ch <- result{err: errClosed}
			return
		}
		if err := writeFrame(w.stdin, req); err != nil {
			ch <- result{err: err}
			return
		}
		resp, err := w.readResponseLocked(0)
		ch <- result{resp, err}
	}()

	if ctx == nil {
		r := <-ch
		return unwrap(r.resp, r.err)
	}
	select {
	case r := <-ch:
		return unwrap(r.resp, r.err)
	case <-ctx.Done():
		// Kill the child to unblock the in-flight read, then report
		// cancellation. The goroutine above releases ioMu on its own.
		_ = w.kill()
		return Response{}, ctx.Err()
	}
}

func unwrap(r Response, err error) (Response, error) {
	if err != nil {
		return Response{}, err
	}
	switch r.Status {
	case "ok":
		return r, nil
	case "error":
		return Response{}, errors.New("worker: " + r.Message)
	case "fatal":
		return Response{}, errors.New("worker fatal: " + r.Message)
	default:
		return Response{}, fmt.Errorf("worker: unexpected status %q", r.Status)
	}
}

// readResponse is the unlocked entry point used during startup, before any
// calls can race. timeout==0 waits indefinitely.
func (w *Worker) readResponse(timeout time.Duration) (Response, error) {
	return w.readResponseLocked(timeout)
}

// readResponseLocked reads one response. The caller must hold ioMu.
func (w *Worker) readResponseLocked(timeout time.Duration) (Response, error) {
	if w.readErr != nil {
		return Response{}, w.readErr
	}

	type result struct {
		resp Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var resp Response
		err := readFrame(w.stdout, &resp)
		ch <- result{resp, err}
	}()

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, io.EOF) {
				r.err = errors.New("worker: child exited unexpectedly")
			}
			w.readErr = r.err
			return Response{}, r.err
		}
		return r.resp, nil
	case <-timer:
		return Response{}, errors.New("worker: timed out waiting for a response")
	}
}

// There is deliberately no `case <-w.done` in the select above. A child that
// writes its final frame and exits immediately — which is exactly what a
// startup failure does — closes w.done at almost the same moment, and the
// select could take that branch before the reader goroutine was ever
// scheduled, discarding a response that was already in the pipe. The EOF the
// reader reports when the pipe closes produces the same error, but only after
// any buffered frame has been consumed, so it is both correct and race-free.
// Regression test: TestWorkerKeepsTheFinalFrameWhenTheChildExits.

// kill terminates the child and waits for it to be reaped. It deliberately
// takes no locks so it can be called while a call holds ioMu.
func (w *Worker) kill() error {
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	<-w.done
	return nil
}

func resolveWorkerPath(hint string) (string, error) {
	if hint != "" {
		if _, err := os.Stat(hint); err == nil {
			return hint, nil
		}
		return "", fmt.Errorf("worker: explicit path %q not found", hint)
	}
	if env := os.Getenv("NEEDLE_WORKER"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
		return "", fmt.Errorf("worker: NEEDLE_WORKER=%q not found", env)
	}
	if exe, err := os.Executable(); err == nil {
		for _, name := range []string{"needle-worker", "needle-worker.exe"} {
			candidate := filepath.Join(filepath.Dir(exe), name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	if p, err := exec.LookPath("needle-worker"); err == nil {
		return p, nil
	}
	return "", errors.New("worker: cannot find needle-worker; set NEEDLE_WORKER or pass an explicit path")
}
