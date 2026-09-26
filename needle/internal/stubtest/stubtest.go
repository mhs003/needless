// Package stubtest builds the throwaway artifacts the binding's tests need:
// a stub engine shared library (from stub.c) and the needle-worker binary.
//
// Everything it produces lands in the test's temporary directory, so tests are
// hermetic and never touch the real engine or model.
package stubtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// ModuleRoot walks up from this file to the directory holding go.mod.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("stubtest: runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("stubtest: go.mod not found above " + file)
		}
		dir = parent
	}
}

// NeedleDir is the directory of the needle package (the module's root + /needle).
func NeedleDir(t testing.TB) string {
	return filepath.Join(ModuleRoot(t), "needle")
}

// EngineDir holds needle.h, used to prototype-check stub.c at compile time.
func EngineDir(t testing.TB) string {
	return filepath.Join(NeedleDir(t), "engine")
}

// BuildStub compiles stub.c into a shared library and returns its path.
func BuildStub(t testing.TB) string {
	t.Helper()
	compiler, err := exec.LookPath("cc")
	if err != nil {
		compiler, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("stubtest: no C compiler (cc/gcc) available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "libneedle.so")
	cmd := exec.Command(compiler,
		"-shared", "-fPIC", "-O1", "-Wall", "-Wextra",
		"-I", EngineDir(t),
		filepath.Join(filepath.Dir(selfFile(t)), "testdata", "stub.c"),
		"-o", out,
	)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stubtest: cc failed: %v\n%s", err, combined)
	}
	return out
}

// BuildWorker compiles the needle-worker binary once per test binary and
// returns its path.
var (
	workerOnce sync.Once
	workerPath string
	workerErr  error
)

func BuildWorker(t testing.TB) string {
	t.Helper()
	workerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "needle-worker-build-")
		if err != nil {
			workerErr = err
			return
		}
		out := filepath.Join(dir, "needle-worker")
		cmd := exec.Command("go", "build", "-o", out, "./needle/cmd/needle-worker")
		cmd.Dir = ModuleRoot(t)
		if combined, err := cmd.CombinedOutput(); err != nil {
			workerErr = err
			t.Logf("stubtest: go build worker failed:\n%s", combined)
			return
		}
		workerPath = out
	})
	if workerErr != nil {
		t.Fatalf("stubtest: build worker: %v", workerErr)
	}
	return workerPath
}

// WriteWeights writes a minimal .cact envelope whose 5th byte is the model id.
// The stub validates the magic and records the id, so tests can prove the
// right archive reached the right worker.
func WriteWeights(t testing.TB, dir string, modelID byte) string {
	t.Helper()
	path := filepath.Join(dir, "model.cact")
	// 0x05E12A83 little-endian, then the model id, then a filler byte.
	data := []byte{0x83, 0x2A, 0xE1, 0x05, modelID, 0x01}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("stubtest: write weights: %v", err)
	}
	return path
}

// WriteWeightsAt is WriteWeights with a caller-chosen file name.
func WriteWeightsAt(t testing.TB, path string, modelID byte) string {
	t.Helper()
	data := []byte{0x83, 0x2A, 0xE1, 0x05, modelID, 0x01}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("stubtest: write weights: %v", err)
	}
	return path
}

func selfFile(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("stubtest: runtime.Caller failed")
	}
	return file
}
