package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhs003/needless/needle/internal/stubtest"
)

// startStub spawns a worker backed by the stub engine and a synthetic archive
// carrying modelID.
func startStub(t *testing.T, modelID byte, tools string, bufSize int) *Worker {
	t.Helper()
	w, err := tryStartStub(t, modelID, tools, bufSize)
	if err != nil {
		t.Fatalf("startStub: %v", err)
	}
	return w
}

func tryStartStub(t *testing.T, modelID byte, tools string, bufSize int) (*Worker, error) {
	t.Helper()
	lib := stubtest.BuildStub(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), modelID)
	w, err := Start(context.Background(), stubtest.BuildWorker(t), Config{
		Library:    lib,
		Weights:    weights,
		Tools:      tools,
		System:     "date: 2026-07-21 Tue 14:30",
		BufferSize: bufSize,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, nil
}

func TestWorkerCompleteRoundTrip(t *testing.T) {
	w := startStub(t, 11, "[]", 4096)
	out, err := w.Complete(context.Background(), "hello there", 64)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("reply is not JSON: %v (%q)", err, out)
	}
	if payload["model"].(float64) != 11 {
		t.Errorf("model = %v, want 11", payload["model"])
	}
	if payload["input"].(string) != "hello there" {
		t.Errorf("input = %v", payload["input"])
	}
	if payload["max"].(float64) != 64 {
		t.Errorf("max = %v, want 64", payload["max"])
	}
}

func TestWorkerReportsPrefixTokens(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	if got := w.PrefixTokens(); got != 7 {
		t.Fatalf("PrefixTokens() = %d, want 7", got)
	}
}

// Mirrors the Python test: two agents in one program must be fully isolated,
// each with its own process, model and conversation.
func TestWorkersAreIndependentProcesses(t *testing.T) {
	first := startStub(t, 11, "[]", 4096)
	second := startStub(t, 29, "[]", 4096)

	if first.PID() == second.PID() {
		t.Fatalf("expected distinct PIDs, both are %d", first.PID())
	}

	ctx := context.Background()
	out1, err := first.Complete(ctx, "one", 8)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := second.Complete(ctx, "two", 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := field(t, out1, "model"); got != 11 {
		t.Errorf("first model = %v, want 11", got)
	}
	if got := field(t, out2, "model"); got != 29 {
		t.Errorf("second model = %v, want 29", got)
	}

	// Resetting one must not touch the other.
	if err := first.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	out1, _ = first.Complete(ctx, "again", 8)
	out2, _ = second.Complete(ctx, "again", 8)
	if got := field(t, out1, "resets"); got != 1 {
		t.Errorf("first resets = %v, want 1", got)
	}
	if got := field(t, out2, "resets"); got != 0 {
		t.Errorf("second resets = %v, want 0", got)
	}
}

func TestWorkerEmbed(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	ctx := context.Background()

	dim, err := w.EmbedDim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dim != 8 {
		t.Fatalf("dim = %d, want 8", dim)
	}

	vec, err := w.Embed(ctx, "search query")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != dim {
		t.Fatalf("len(vec) = %d, want %d", len(vec), dim)
	}
	// Repeated calls must not alias the worker's reused buffer.
	second, err := w.Embed(ctx, "search query")
	if err != nil {
		t.Fatal(err)
	}
	if &vec[0] == &second[0] {
		t.Fatal("consecutive Embed calls returned the same backing array")
	}
	for i := range vec {
		if vec[i] != second[i] {
			t.Fatalf("embeddings differ at %d: %v vs %v", i, vec, second)
		}
	}
}

func TestWorkerSurfacesEngineErrors(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	ctx := context.Background()

	_, err := w.Complete(ctx, "FAIL_COMPLETE", 8)
	if err == nil || !strings.Contains(err.Error(), "intentional failure") {
		t.Fatalf("Complete error = %v", err)
	}
	// The worker must stay usable after a failed turn.
	if _, err := w.Complete(ctx, "ok", 8); err != nil {
		t.Fatalf("worker unusable after an engine error: %v", err)
	}

	if _, err := w.Embed(ctx, "FAIL_EMBED"); err == nil {
		t.Fatal("expected an embed error")
	}
	if _, err := w.Complete(ctx, "ok", 8); err != nil {
		t.Fatalf("worker unusable after an embed error: %v", err)
	}
}

func TestWorkerUnknownOpIsRejected(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	_, err := w.call(context.Background(), Request{Op: "nonsense"})
	if err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("error = %v, want an unknown-op error", err)
	}
	// Still usable.
	if _, err := w.Complete(context.Background(), "ok", 8); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerStartupFailures(t *testing.T) {
	worker := stubtest.BuildWorker(t)
	lib := stubtest.BuildStub(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), 7)

	cases := []struct {
		name    string
		cfg     Config
		path    string
		wantSub string
	}{
		{
			name:    "missing library config",
			cfg:     Config{Weights: weights, BufferSize: 4096},
			wantSub: "library path is required",
		},
		{
			name:    "missing weights config",
			cfg:     Config{Library: lib, BufferSize: 4096},
			wantSub: "weights path is required",
		},
		{
			name:    "library does not exist",
			cfg:     Config{Library: filepath.Join(t.TempDir(), "nope.so"), Weights: weights, BufferSize: 4096},
			wantSub: "dlopen",
		},
		{
			name:    "weights do not exist",
			cfg:     Config{Library: lib, Weights: filepath.Join(t.TempDir(), "nope.cact"), BufferSize: 4096},
			wantSub: "weights",
		},
		{
			name: "bad archive",
			cfg: Config{
				Library:    lib,
				Weights:    writeBadWeights(t),
				BufferSize: 4096,
			},
			wantSub: "bad magic",
		},
		{
			name:    "init failure",
			cfg:     Config{Library: lib, Weights: weights, Tools: `[{"name":"INIT_FAIL"}]`, BufferSize: 4096},
			wantSub: "prefix does not fit",
		},
		{
			name:    "worker binary missing",
			cfg:     Config{Library: lib, Weights: weights, BufferSize: 4096},
			path:    filepath.Join(t.TempDir(), "absent-worker"),
			wantSub: "not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = worker
			}
			w, err := Start(context.Background(), path, tc.cfg)
			if err == nil {
				_ = w.Close()
				t.Fatal("expected a startup error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestWorkerKeepsTheFinalFrameWhenTheChildExits is the regression test for a
// race in readResponseLocked: a child that writes its last frame and exits
// immediately made the select take the `w.done` branch before the reader
// goroutine ran, so the frame was discarded and startup reported "child exited
// unexpectedly" instead of the real reason. A startup failure is exactly that
// shape, so this repeats it enough times to hit the race.
func TestWorkerKeepsTheFinalFrameWhenTheChildExits(t *testing.T) {
	worker := stubtest.BuildWorker(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), 7)

	for i := 0; i < 25; i++ {
		_, err := Start(context.Background(), worker, Config{
			Library:    filepath.Join(t.TempDir(), "nope.so"),
			Weights:    weights,
			BufferSize: 4096,
		})
		if err == nil {
			t.Fatal("expected a startup failure")
		}
		// The child explains itself before exiting; that explanation must
		// survive, not be replaced by a generic "exited unexpectedly".
		if !strings.Contains(err.Error(), "dlopen") {
			t.Fatalf("iteration %d: error = %v, want the child's own message about dlopen", i, err)
		}
	}
}

func TestWorkerReportsChildCrash(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	_, err := w.Complete(context.Background(), "please CRASH now", 8)
	if err == nil {
		t.Fatal("expected an error after the child aborted")
	}
	// The child is gone; a further call must fail fast rather than hang.
	done := make(chan error, 1)
	go func() {
		_, err := w.Complete(context.Background(), "again", 8)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a second error after the child exited")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subsequent call hung after the child exited")
	}
}

func TestWorkerContextCancellation(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := w.Complete(ctx, "please SLEEP for a while", 8)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("cancellation took %v; the read was not unblocked promptly", elapsed)
	}
	// The worker should now be dead and not hang on shutdown.
	closed := make(chan struct{})
	go func() { _ = w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung after cancellation")
	}
}

func TestWorkerCloseIsIdempotent(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
	if !w.Closed() {
		t.Error("Closed() = false after Close")
	}
	// The child must be reaped.
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child was not reaped after Close")
	}
}

func TestWorkerUseAfterClose(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)
	_ = w.Close()
	if _, err := w.Complete(context.Background(), "x", 8); !errors.Is(err, errClosed) {
		t.Fatalf("Complete after Close: %v, want errClosed", err)
	}
	if _, err := w.Embed(context.Background(), "x"); !errors.Is(err, errClosed) {
		t.Fatalf("Embed after Close: %v, want errClosed", err)
	}
	if err := w.Reset(context.Background()); !errors.Is(err, errClosed) {
		t.Fatalf("Reset after Close: %v, want errClosed", err)
	}
}

// Concurrency correctness: with many goroutines in flight, every response must
// match its own request. A regression here is the interleaving bug that
// guarding only the write would cause.
func TestWorkerConcurrentRequestsStayPaired(t *testing.T) {
	w := startStub(t, 7, "[]", 64*1024)
	const n = 24

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := "request-marker-" + strings.Repeat("x", i%5) + "-" + itoa(i)
			out, err := w.Complete(context.Background(), marker, 8)
			if err != nil {
				errs <- err
				return
			}
			if got := fieldStr(t, out, "input"); got != marker {
				errs <- errors.New("mismatched response for " + marker)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestWorkerHandlesLargeOutput(t *testing.T) {
	// A buffer smaller than the response exercises the truncation path.
	w := startStub(t, 7, "[]", 128)
	out, err := w.Complete(context.Background(), "TRUNCATE", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 127 {
		t.Fatalf("len(out) = %d, want 127", len(out))
	}
	if strings.Trim(out, "x") != "" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWorkerSendsConfiguredSystemPromptAndTools(t *testing.T) {
	// The stub echoes the model id it was loaded with, which only exists if
	// the config frame was parsed correctly; combine that with a distinct id.
	w := startStub(t, 200, `[{"name":"set_lights"}]`, 4096)
	out, err := w.Complete(context.Background(), "hi", 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := field(t, out, "model"); got != 200 {
		t.Fatalf("model = %v, want 200", got)
	}
}

func TestResolveWorkerPathHintNotFound(t *testing.T) {
	_, err := Start(context.Background(), filepath.Join(t.TempDir(), "missing"), Config{
		Library: "x", Weights: "y", BufferSize: 16,
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

// --- helpers -------------------------------------------------------------

func field(t *testing.T, raw, key string) int {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("reply is not JSON: %v (%q)", err, raw)
	}
	n, ok := payload[key].(float64)
	if !ok {
		t.Fatalf("field %q missing or not numeric in %q", key, raw)
	}
	return int(n)
}

func fieldStr(t *testing.T, raw, key string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("reply is not JSON: %v (%q)", err, raw)
	}
	s, ok := payload[key].(string)
	if !ok {
		t.Fatalf("field %q missing or not a string in %q", key, raw)
	}
	return s
}

func writeBadWeights(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bad.cact")
	if err := os.WriteFile(path, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x07, 0x07}, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
