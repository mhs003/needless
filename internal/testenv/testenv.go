// Package testenv builds the project's binaries and locates the real model for
// tests that need them.
//
// It exists because the binding's own helpers live in
// `needle/internal/stubtest`, and Go's internal-package rule means nothing
// outside the `needle/` tree may import them — `internal/cli` and `cmd/n` are
// outside it. `internal/` sits at the module root, so this is reachable from
// both.
//
// Only test files import this package. It is not part of what `n` ships.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhs003/needless/needle"
)

// ModelFileName is the archive `make doctor` looks for.
const ModelFileName = "needle3.cact"

// ModuleRoot returns the module root, found by walking up for go.mod.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("testenv: working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("testenv: no go.mod above %s", dir)
		}
		dir = parent
	}
}

var (
	binMu   sync.Mutex
	binPath = map[string]string{}
	binErr  = map[string]error{}
)

// Binary builds pkgPath (relative to the module root, e.g. "./cmd/n") once per
// test binary and returns the executable's path.
//
// The output directory deliberately outlives t.TempDir: the binary is shared by
// every test in the process, so it cannot belong to any one of them.
func Binary(t testing.TB, pkgPath, name string) string {
	t.Helper()

	binMu.Lock()
	defer binMu.Unlock()

	if err, ok := binErr[pkgPath]; ok {
		t.Fatalf("testenv: build %s: %v", pkgPath, err)
	}
	if path, ok := binPath[pkgPath]; ok {
		return path
	}

	dir, err := os.MkdirTemp("", "needless-build-")
	if err != nil {
		t.Fatalf("testenv: %v", err)
	}
	out := filepath.Join(dir, name)

	cmd := exec.Command("go", "build", "-o", out, pkgPath)
	cmd.Dir = ModuleRoot(t)
	if combined, err := cmd.CombinedOutput(); err != nil {
		binErr[pkgPath] = fmt.Errorf("%w\n%s", err, combined)
		t.Fatalf("testenv: build %s: %v\n%s", pkgPath, err, combined)
	}

	binPath[pkgPath] = out
	return out
}

// RealModel returns the path to a real .cact archive, or "" when there is none.
//
// It honours NEEDLE_MODEL first, then walks up from the module root looking for
// models/<ModelFileName>. The walk means the lookup survives the test being run
// from any package directory.
//
// This duplicates the binding's realModelPath across the internal-package
// boundary on purpose: sharing it would mean exporting a binding-internal
// helper purely for tests.
func RealModel(t testing.TB) string {
	t.Helper()
	if env := os.Getenv("NEEDLE_MODEL"); env != "" {
		return env
	}

	dir := ModuleRoot(t)
	for {
		candidate := filepath.Join(dir, "models", ModelFileName)
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

// Engine reports whether an engine for this platform can be found, and where.
func Engine() (string, bool) {
	path, err := needle.DefaultEnginePath()
	if err != nil {
		return "", false
	}
	return path, true
}

// RequireRealModel skips the test unless the real model and engine are both
// available, and returns the model path.
//
// Slow tests go through here so that a bare checkout still passes: the archive
// is 34 MiB and is not committed.
func RequireRealModel(t testing.TB) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping a real-model test in short mode")
	}
	if _, ok := Engine(); !ok {
		t.Skip("no engine is available for this platform")
	}
	model := RealModel(t)
	if model == "" {
		t.Skipf("no %s found; set NEEDLE_MODEL to point at one", ModelFileName)
	}
	return model
}

// TopicCount is how many commands the scale tests generate. Five or so is the
// size the shipped examples exercise; this is large enough that the engine's
// retrieval head has to choose rather than render everything.
const TopicCount = 60

// ScaleTopics are the distinct subjects the generated commands cover. They are
// deliberately unrelated words, so a prompt naming one is not a near-tie with
// any other.
var ScaleTopics = []string{
	"weather", "calendar", "email", "invoices", "backups",
	"deployments", "databases", "certificates", "logs", "metrics",
	"alerts", "pipelines", "tickets", "contacts", "expenses",
	"payroll", "inventory", "shipping", "billing", "subscriptions",
	"analytics", "dashboards", "notifications", "webhooks", "integrations",
	"permissions", "sessions", "secrets", "containers", "clusters",
	"buckets", "queues", "schemas", "migrations", "replicas",
	"snapshots", "archives", "indexes", "caches", "proxies",
	"gateways", "tunnels", "firewalls", "domains", "mailboxes",
	"printers", "scanners", "cameras", "sensors", "thermostats",
	"locks", "lights", "speakers", "displays", "monitors",
	"keyboards", "routers", "switches", "modems", "satellites",
}

// ScaleCommand returns the id and source of the i'th generated command.
//
// Its run block prints "ran:<id>", so a test can tell which command actually
// executed rather than inferring it.
func ScaleCommand(i int) (id, source string) {
	topic := ScaleTopics[i%len(ScaleTopics)]
	id = "gen/" + topic
	source = fmt.Sprintf(`instruction """
Check the %s subsystem and report its status.

Use this when the user asks about %s.
"""

confirm false

run {
    print("ran:%s")
}
`, topic, topic, id)
	return id, source
}
