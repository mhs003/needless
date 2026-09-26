package abi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhs003/needless/needle/internal/stubtest"
)

// newStub opens a fresh stub engine and loads a model with the given id.
func newStub(t *testing.T, modelID byte, tools string) *Engine {
	t.Helper()
	lib := stubtest.BuildStub(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), modelID)

	e, err := Open(lib)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	data := mustReadFile(t, weights)
	if err := e.Load(data); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Init("", tools, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return e
}

func TestOpenMissingLibrary(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope.so"))
	if err == nil {
		t.Fatal("expected an error opening a missing library")
	}
	if !strings.Contains(err.Error(), "dlopen") {
		t.Errorf("error should mention dlopen, got: %v", err)
	}
}

func TestOpenRealEngine(t *testing.T) {
	// The vendored production engine must dlopen and resolve every symbol.
	path := filepath.Join(stubtest.EngineDir(t), "linux-x86_64", "libneedle.so")
	e, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer e.Close()
	for name, fn := range map[string]any{
		"load": e.loadFn, "init": e.initFn, "complete": e.complFn,
		"embed": e.embedFn, "reset": e.resetFn, "last_error": e.lasterrFn,
	} {
		if fn == nil {
			t.Errorf("symbol %s was not resolved", name)
		}
	}
}

func TestLoadRejectsEmptyAndBadArchives(t *testing.T) {
	lib := stubtest.BuildStub(t)
	e, err := Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if err := e.Load(nil); err == nil {
		t.Error("Load(nil) should fail")
	}
	if err := e.Load([]byte{}); err == nil {
		t.Error("Load(empty) should fail")
	}
	// Wrong magic: the stub sets a descriptive last_error.
	err = e.Load([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x01})
	if err == nil {
		t.Fatal("Load(bad magic) should fail")
	}
	if !strings.Contains(err.Error(), "bad magic") {
		t.Errorf("error should quote last_error, got: %v", err)
	}
	// Too short.
	if err := e.Load([]byte{0x83, 0x2A}); err == nil {
		t.Error("Load(short) should fail")
	}
	// Valid archive.
	if err := e.Load([]byte{0x83, 0x2A, 0xE1, 0x05, 0x2A, 0x01}); err != nil {
		t.Errorf("Load(valid) failed: %v", err)
	}
}

func TestInitReturnValues(t *testing.T) {
	lib := stubtest.BuildStub(t)
	weights := stubtest.WriteWeights(t, t.TempDir(), 7)
	e, err := Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Load(mustReadFile(t, weights)); err != nil {
		t.Fatal(err)
	}

	n, err := e.Init("", "[]", "")
	if err != nil || n != 7 {
		t.Fatalf("Init normal: n=%d err=%v, want n=7", n, err)
	}

	_, err = e.Init("", `[{"name":"INIT_FAIL"}]`, "")
	if err == nil || !strings.Contains(err.Error(), "prefix does not fit") {
		t.Fatalf("Init failure should surface last_error, got: %v", err)
	}

	n, err = e.Init("", `[{"name":"INIT_BIG_PREFIX"}]`, "")
	if err != nil || n != 1000000 {
		t.Fatalf("Init big prefix: n=%d err=%v, want 1000000", n, err)
	}
}

func TestCompleteValidation(t *testing.T) {
	e := newStub(t, 7, "[]")
	buf := make([]byte, 1024)

	if _, err := e.Complete("hi", 0, buf); err == nil {
		t.Error("maxNewTokens=0 should be rejected")
	}
	if _, err := e.Complete("hi", -5, buf); err == nil {
		t.Error("negative maxNewTokens should be rejected")
	}
	if _, err := e.Complete("hi", 8, nil); err == nil {
		t.Error("a nil buffer should be rejected")
	}
	if _, err := e.Complete("hi", 8, []byte{}); err == nil {
		t.Error("an empty buffer should be rejected")
	}
}

func TestCompleteRoundTrip(t *testing.T) {
	e := newStub(t, 42, "[]")
	buf := make([]byte, 4096)

	out, err := e.Complete("dim the lights", 128, buf)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, out)
	}
	if got["model"].(float64) != 42 {
		t.Errorf("model id = %v, want 42", got["model"])
	}
	if got["input"].(string) != "dim the lights" {
		t.Errorf("input echoed as %v", got["input"])
	}
	if got["max"].(float64) != 128 {
		t.Errorf("max_new_tokens = %v, want 128", got["max"])
	}
}

func TestCompleteFailureSurfacesLastError(t *testing.T) {
	e := newStub(t, 7, "[]")
	_, err := e.Complete("please FAIL_COMPLETE", 8, make([]byte, 1024))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Errorf("error should quote last_error, got: %v", err)
	}
}

func TestCompleteNULTerminationEdgeCases(t *testing.T) {
	e := newStub(t, 7, "[]")

	t.Run("truncated output fills buffer exactly", func(t *testing.T) {
		buf := make([]byte, 64)
		out, err := e.Complete("TRUNCATE", 8, buf)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 63 {
			t.Fatalf("len(out) = %d, want 63 (capacity-1)", len(out))
		}
		if strings.Trim(out, "x") != "" {
			t.Errorf("output should be all 'x', got %q", out)
		}
	})

	t.Run("missing terminator does not panic", func(t *testing.T) {
		buf := make([]byte, 32)
		out, err := e.Complete("NO_TERMINATOR", 8, buf)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 32 {
			t.Fatalf("len(out) = %d, want the full 32-byte buffer", len(out))
		}
	})

	t.Run("bogus return value is ignored", func(t *testing.T) {
		// The engine claims 999999 bytes but wrote "{}". Complete must go
		// by the NUL terminator, not the return value.
		out, err := e.Complete("RETURN_BOGUS", 8, make([]byte, 128))
		if err != nil {
			t.Fatal(err)
		}
		if out != "{}" {
			t.Fatalf("out = %q, want %q", out, "{}")
		}
	})

	t.Run("embedded NUL cuts the string", func(t *testing.T) {
		out, err := e.Complete("NUL_EMBEDDED", 8, make([]byte, 128))
		if err != nil {
			t.Fatal(err)
		}
		if out != "a" {
			t.Fatalf("out = %q, want %q", out, "a")
		}
	})

	t.Run("buffer is cleared between calls", func(t *testing.T) {
		buf := make([]byte, 64)
		if _, err := e.Complete("first: long response to fill the buffer", 8, buf); err != nil {
			t.Fatal(err)
		}
		// A later call that writes nothing must not expose the old bytes.
		out, err := e.Complete("", 8, buf)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "first") {
			t.Fatalf("stale bytes leaked from the previous call: %q", out)
		}
	})
}

func TestCompletePreservesUTF8(t *testing.T) {
	e := newStub(t, 7, "[]")
	out, err := e.Complete("give me UTF8", 8, make([]byte, 4096))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("reply is not JSON: %v (%q)", err, out)
	}
	const want = "héllo 世界 😀"
	if payload.Text != want {
		t.Fatalf("text = %q, want %q", payload.Text, want)
	}
}

func TestEmbedDimensionAndVector(t *testing.T) {
	e := newStub(t, 7, "[]")

	dim, err := e.EmbedDim()
	if err != nil {
		t.Fatal(err)
	}
	if dim != 8 {
		t.Fatalf("dim = %d, want 8", dim)
	}
	// Cached: a second call returns the same value.
	if again, _ := e.EmbedDim(); again != dim {
		t.Fatalf("cached dim = %d, want %d", again, dim)
	}

	vec := make([]float32, dim)
	n, err := e.Embed("abc", vec)
	if err != nil {
		t.Fatal(err)
	}
	if n != dim {
		t.Fatalf("Embed wrote %d floats, want %d", n, dim)
	}
	// Deterministic: same input, same vector.
	second := make([]float32, dim)
	if _, err := e.Embed("abc", second); err != nil {
		t.Fatal(err)
	}
	for i := range vec {
		if vec[i] != second[i] {
			t.Fatalf("embedding not deterministic at %d: %v vs %v", i, vec, second)
		}
	}
}

func TestEmbedErrors(t *testing.T) {
	e := newStub(t, 7, "[]")
	if _, err := e.Embed("x", nil); err == nil {
		t.Error("a nil buffer should be rejected")
	}
	if _, err := e.Embed("x", []float32{}); err == nil {
		t.Error("an empty buffer should be rejected")
	}
	_, err := e.Embed("FAIL_EMBED", make([]float32, 8))
	if err == nil || !strings.Contains(err.Error(), "intentional failure") {
		t.Fatalf("expected last_error on embed failure, got: %v", err)
	}
}

func TestResetAdvancesConversationState(t *testing.T) {
	e := newStub(t, 7, "[]")
	buf := make([]byte, 4096)

	before := completeResets(t, e, buf)
	e.Reset()
	e.Reset()
	after := completeResets(t, e, buf)
	if after < before+2 {
		t.Fatalf("resets went %d -> %d, expected at least two more", before, after)
	}
}

func TestEngineCloseIsIdempotentAndDisablesMethods(t *testing.T) {
	lib := stubtest.BuildStub(t)
	e, err := Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	buf := make([]byte, 64)
	if _, err := e.Complete("x", 8, buf); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Complete after Close: got %v, want ErrNotOpen", err)
	}
	if err := e.Load([]byte{0x83, 0x2A, 0xE1, 0x05, 1, 1}); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Load after Close: got %v, want ErrNotOpen", err)
	}
	if _, err := e.Embed("x", make([]float32, 8)); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Embed after Close: got %v, want ErrNotOpen", err)
	}
	if _, err := e.Init("", "[]", ""); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Init after Close: got %v, want ErrNotOpen", err)
	}
	// Reset after Close must be a no-op, not a crash.
	e.Reset()
}

func TestConcurrentCallsAreSerialised(t *testing.T) {
	e := newStub(t, 7, "[]")
	const workers = 8
	done := make(chan string, workers)
	for i := 0; i < workers; i++ {
		go func() {
			out, err := e.Complete("concurrent", 8, make([]byte, 4096))
			if err != nil {
				done <- "ERR:" + err.Error()
				return
			}
			done <- out
		}()
	}
	for i := 0; i < workers; i++ {
		got := <-done
		if strings.HasPrefix(got, "ERR:") {
			t.Fatalf("concurrent complete failed: %s", got)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(got), &payload); err != nil {
			t.Fatalf("reply is not JSON: %v (%q)", err, got)
		}
	}
}

func TestLastErrorIsCopiedNotAliased(t *testing.T) {
	e := newStub(t, 7, "[]")
	_, _ = e.Complete("FAIL_COMPLETE", 8, make([]byte, 64))
	first := e.LastError()
	if !strings.Contains(first, "intentional failure") {
		t.Fatalf("LastError() = %q", first)
	}
	// Another call invalidates the engine-owned pointer; our copy must
	// remain intact.
	_, _ = e.Complete("ok", 8, make([]byte, 64))
	if !strings.Contains(first, "intentional failure") {
		t.Fatalf("previously returned error string was mutated: %q", first)
	}
}

func completeResets(t *testing.T, e *Engine, buf []byte) int {
	t.Helper()
	out, err := e.Complete("state", 8, buf)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Resets int `json:"resets"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("reply is not JSON: %v (%q)", err, out)
	}
	return payload.Resets
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
