package needle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mhs003/needless/needle/internal/stubtest"
)

// --- helpers -------------------------------------------------------------

func stubConfig(t *testing.T, modelID byte, tools string) Config {
	t.Helper()
	return Config{
		EnginePath:  stubtest.BuildStub(t),
		WeightsPath: stubtest.WriteWeights(t, t.TempDir(), modelID),
		ToolsJSON:   tools,
		BufferSize:  64 * 1024,
	}
}

func newStubNeedle(t *testing.T, modelID byte, tools string) *Needle {
	t.Helper()
	cfg := stubConfig(t, modelID, tools)
	n, err := newWithWorker(t, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// newWithWorker calls New with the pre-built worker binary so tests never
// depend on discovery.
func newWithWorker(t *testing.T, cfg Config) (*Needle, error) {
	t.Helper()
	cfg.WorkerPath = stubtest.BuildWorker(t)
	return New(context.Background(), cfg)
}

// --- tests ---------------------------------------------------------------

func TestNewRejectsBadConfig(t *testing.T) {
	ctx := context.Background()
	worker := stubtest.BuildWorker(t)
	lib := stubtest.BuildStub(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), 7)

	t.Run("no weights path", func(t *testing.T) {
		_, err := New(ctx, Config{EnginePath: lib, WorkerPath: worker})
		if err == nil || !strings.Contains(err.Error(), "WeightsPath is required") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("weights missing on disk", func(t *testing.T) {
		_, err := New(ctx, Config{
			EnginePath:  lib,
			WeightsPath: filepath.Join(t.TempDir(), "gone.cact"),
			WorkerPath:  worker,
		})
		if err == nil || !strings.Contains(err.Error(), "weights") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("explicit engine missing", func(t *testing.T) {
		_, err := New(ctx, Config{
			EnginePath:  filepath.Join(t.TempDir(), "gone.so"),
			WeightsPath: weights,
			WorkerPath:  worker,
		})
		if err == nil || !strings.Contains(err.Error(), "engine") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDefaultEnginePathFindsVendoredEngine(t *testing.T) {
	// The repository ships the engine for this platform, so discovery must
	// find it. This test used to skip when discovery failed, which hid two
	// real bugs at once: the vendored path was resolved one directory too
	// high, and it used Go's platform spelling ("linux-amd64") where upstream
	// names the directory "linux-x86_64". The Python package cache on the
	// developer's machine was covering for both.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this source file")
	}
	vendored := filepath.Join(filepath.Dir(thisFile), "engine", platformDirs()[0], libraryName())
	if _, err := os.Stat(vendored); err != nil {
		t.Skipf("no engine is vendored for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	got, err := DefaultEnginePath()
	if err != nil {
		t.Fatalf("the engine is vendored at %s but discovery did not find it: %v", vendored, err)
	}
	if got != vendored {
		t.Errorf("DefaultEnginePath() = %q, want the vendored %q", got, vendored)
	}
}

// TestEngineNotFoundNamesThePlatformDirectory guards the one thing that makes
// the message useful. Go and upstream spell platforms differently — linux/amd64
// against linux-x86_64 — so a message that only says "set NEEDLE_ENGINE" leaves
// the reader to guess the very string that is easy to get wrong.
func TestEngineNotFoundNamesThePlatformDirectory(t *testing.T) {
	err := engineNotFound()
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()

	for _, want := range []string{
		runtime.GOOS,
		runtime.GOARCH,
		platformDirs()[0], // the upstream spelling, e.g. linux-x86_64
		libraryName(),     // e.g. libneedle.so
		"NEEDLE_ENGINE",
		"BINDINGS.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}

	// It must be specific, not a restatement of the Go platform: on
	// linux/amd64 the directory is linux-x86_64 and those differ.
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if !strings.Contains(msg, "linux-x86_64") {
			t.Errorf("message omits the upstream directory name:\n%s", msg)
		}
	}
}

func TestDefaultEnginePathHonoursEnv(t *testing.T) {
	lib := stubtest.BuildStub(t)
	t.Setenv("NEEDLE_ENGINE", lib)
	got, err := DefaultEnginePath()
	if err != nil {
		t.Fatal(err)
	}
	if got != lib {
		t.Fatalf("got %q, want %q", got, lib)
	}
}

func TestCompleteAndParseResult(t *testing.T) {
	// The stub returns a plain JSON object with an "input" field, which is
	// enough to exercise the transport and the decoder.
	n := newStubNeedle(t, 7, "[]")
	raw, err := n.Complete(context.Background(), "hello", 32)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("raw reply is not JSON: %v (%q)", err, raw)
	}

	res, err := ParseResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The stub emits type "text"; what matters here is that a payload with no
	// function_calls reads as a refusal.
	if !res.Refusal() {
		t.Error("a payload without function_calls should read as a refusal")
	}
}

func TestCompleteDefaultsMaxNewTokens(t *testing.T) {
	n := newStubNeedle(t, 7, "[]")
	raw, err := n.Complete(context.Background(), "hi", 0)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Max int `json:"max"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Max != defaultMaxNewTokens {
		t.Fatalf("max = %d, want %d", payload.Max, defaultMaxNewTokens)
	}
}

func TestResetAndPrefixTokens(t *testing.T) {
	n := newStubNeedle(t, 7, "[]")
	if n.PrefixTokens() != 7 {
		t.Errorf("PrefixTokens() = %d, want 7", n.PrefixTokens())
	}
	if err := n.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
}

func TestEmbedThroughPublicAPI(t *testing.T) {
	n := newStubNeedle(t, 7, "[]")
	ctx := context.Background()

	dim, err := n.EmbedDim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dim != 8 {
		t.Fatalf("dim = %d, want 8", dim)
	}
	vec, err := n.Embed(ctx, "route this")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != dim {
		t.Fatalf("len(vec) = %d, want %d", len(vec), dim)
	}
}

func TestNilReceiverIsSafe(t *testing.T) {
	var n *Needle
	if _, err := n.Complete(context.Background(), "x", 8); err == nil {
		t.Error("Complete on a nil *Needle should error")
	}
	if _, err := n.Embed(context.Background(), "x"); err == nil {
		t.Error("Embed on a nil *Needle should error")
	}
	if err := n.Reset(context.Background()); err == nil {
		t.Error("Reset on a nil *Needle should error")
	}
	if err := n.Close(); err != nil {
		t.Errorf("Close on a nil *Needle should be a no-op, got %v", err)
	}
	if n.PID() != -1 {
		t.Error("PID on a nil *Needle should be -1")
	}
}

func TestCloseIsIdempotentAndBlocksFurtherUse(t *testing.T) {
	n := newStubNeedle(t, 7, "[]")
	if err := n.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := n.Complete(context.Background(), "x", 8); err == nil {
		t.Fatal("Complete after Close should fail")
	}
}

// TestConcurrentUseOfOneNeedleIsSafe pins the guarantee that a single Needle
// can be shared across goroutines despite the engine being non-thread-safe.
func TestConcurrentUseOfOneNeedleIsSafe(t *testing.T) {
	n := newStubNeedle(t, 7, "[]")
	const goroutines = 12
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := n.Complete(context.Background(), "shared", 8); err != nil {
				errs <- err
			}
			if _, err := n.Embed(context.Background(), "shared"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestTwoNeedlesInOneProcess(t *testing.T) {
	first := newStubNeedle(t, 11, "[]")
	second := newStubNeedle(t, 29, "[]")

	if first.PID() == second.PID() {
		t.Fatalf("two needles share a worker pid (%d)", first.PID())
	}
	ctx := context.Background()

	a, err := first.Complete(ctx, "a", 8)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Complete(ctx, "b", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a, `"model":11`) {
		t.Errorf("first reply = %q", a)
	}
	if !strings.Contains(b, `"model":29`) {
		t.Errorf("second reply = %q", b)
	}
}

// --- Result decoding (pure Go) -------------------------------------------

func TestParseResultCallShape(t *testing.T) {
	raw := `{
		"type": "call",
		"success": true,
		"error": null,
		"error_code": null,
		"function_calls": [{"name": "set_lights", "arguments": {"room": "living room", "on": true}}],
		"reasoning": "'living room' -> room; 'dim' -> on true",
		"confidence": 0.94,
		"prefill_tps": 4300.0,
		"decode_tps": 850.0
	}`
	res, err := ParseResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "call" || !res.Success {
		t.Fatalf("unexpected header fields: %+v", res)
	}
	if res.Refusal() {
		t.Fatal("a call turn is not a refusal")
	}
	if len(res.FunctionCalls) != 1 || res.FunctionCalls[0].Name != "set_lights" {
		t.Fatalf("function calls = %+v", res.FunctionCalls)
	}
	if res.Confidence == nil || *res.Confidence != 0.94 {
		t.Fatalf("confidence = %v", res.Confidence)
	}

	var args struct {
		Room string `json:"room"`
		On   bool   `json:"on"`
	}
	if err := res.FunctionCalls[0].Bind(&args); err != nil {
		t.Fatal(err)
	}
	if args.Room != "living room" || !args.On {
		t.Fatalf("bound args = %+v", args)
	}
}

func TestParseResultRefusalAndNullConfidence(t *testing.T) {
	raw := `{"type":"respond","success":true,"function_calls":[],"reasoning":"","confidence":null}`
	res, err := ParseResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refusal() {
		t.Error("empty function_calls must read as a refusal")
	}
	if res.Confidence != nil {
		t.Errorf("confidence = %v, want nil", res.Confidence)
	}
}

func TestParseResultSuppressedCalls(t *testing.T) {
	raw := `{"type":"call","function_calls":[],
	         "suppressed_calls":[{"name":"unlock_door","arguments":{"door":"front"}}],
	         "confidence":0.05}`
	res, err := ParseResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refusal() {
		t.Error("a withheld call still means nothing to execute")
	}
	if len(res.SuppressedCalls) != 1 {
		t.Fatalf("suppressed = %+v", res.SuppressedCalls)
	}
}

func TestParseResultRejectsGarbage(t *testing.T) {
	if _, err := ParseResult("not json at all"); err == nil {
		t.Fatal("expected a parse error")
	}
	if _, err := ParseResult(""); err == nil {
		t.Fatal("expected a parse error for empty input")
	}
}

func TestFunctionCallBindWithoutArguments(t *testing.T) {
	c := FunctionCall{Name: "ping"}
	if err := c.Bind(&struct{}{}); err == nil {
		t.Fatal("Bind with no arguments should error")
	}
}

// --- real engine ---------------------------------------------------------

// TestRealEngineSmoke exercises the production engine and the real model. It
// skips unless both are present, so the suite still passes on a bare checkout.
func TestRealEngineSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-engine smoke test in short mode")
	}
	engine, err := DefaultEnginePath()
	if err != nil {
		t.Skipf("engine unavailable: %v", err)
	}
	model := realModelPath(t)
	if model == "" {
		t.Skip("needle3.cact not found; set NEEDLE_MODEL to point at one")
	}

	tools := `[{"name":"set_lights","description":"Turn a room's lights on or off.","parameters":{"type":"object","properties":{"room":{"type":"string"},"on":{"type":"boolean"}},"required":["room","on"]}}]`

	n, err := New(context.Background(), Config{
		EnginePath:   engine,
		WeightsPath:  model,
		ToolsJSON:    tools,
		SystemPrompt: "date: 2026-07-21 Tue 14:30; locale: en-US; device: phone",
		WorkerPath:   stubtest.BuildWorker(t),
	})
	if err != nil {
		t.Fatalf("New with the real engine: %v", err)
	}
	defer n.Close()

	t.Logf("loaded %s, prefix = %d tokens, pid = %d", filepath.Base(model), n.PrefixTokens(), n.PID())
	if n.PrefixTokens() <= 0 {
		t.Errorf("PrefixTokens() = %d, want a positive count", n.PrefixTokens())
	}

	ctx := context.Background()

	// Embedding dimension is a cheap, deterministic check that the engine
	// works end to end.
	dim, err := n.EmbedDim(ctx)
	if err != nil {
		t.Fatalf("EmbedDim: %v", err)
	}
	t.Logf("embedding dimension = %d", dim)
	if dim <= 0 {
		t.Fatalf("dim = %d", dim)
	}
	vec, err := n.Embed(ctx, "dim the living room to 30")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != dim {
		t.Fatalf("len(vec) = %d, want %d", len(vec), dim)
	}
	// Embeddings should not be all zeros.
	var nonzero int
	for _, v := range vec {
		if v != 0 {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Error("embedding is all zeros")
	}

	// A real generation. We do not assert the exact call (the shipped model
	// may decline), only that the engine returns a well-formed envelope.
	res, err := n.CompleteResult(ctx, "dim the living room to 30", 128)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	conf := "nil"
	if res.Confidence != nil {
		conf = strconv.FormatFloat(*res.Confidence, 'f', 3, 64)
	}
	t.Logf("reply: type=%q calls=%d confidence=%s reasoning=%q",
		res.Type, len(res.FunctionCalls), conf, res.Reasoning)
	if res.Type == "" {
		t.Error("reply carried no type")
	}
	for _, c := range res.FunctionCalls {
		t.Logf("  call: %s(%s)", c.Name, c.Arguments)
	}

	// Reset must leave the engine usable.
	if err := n.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := n.Complete(ctx, "off topic request about quantum physics", 64); err != nil {
		t.Fatalf("Complete after Reset: %v", err)
	}
}

func realModelPath(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("NEEDLE_MODEL"); env != "" {
		return env
	}
	// Walk up from the package directory looking for models/needle3.cact, so
	// the lookup survives the binding being moved around the repo.
	dir := stubtest.NeedleDir(t)
	for {
		candidate := filepath.Join(dir, "models", "needle3.cact")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
