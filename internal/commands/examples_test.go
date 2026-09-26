package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhs003/needless/internal/nscript"
)

// examplesDir is the directory name the shipped examples live under.
const examplesDir = "commands"

// examplesRoot walks up to the module root and returns examples/commands.
func examplesRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "examples", examplesDir)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find examples/%s above %s", examplesDir, dir)
		}
		dir = parent
	}
}

// TestShippedExamplesParse keeps the example commands honest. They are
// documentation as much as fixtures, and an example that does not parse is
// worse than no example (AGENTS.md §6).
func TestShippedExamplesParse(t *testing.T) {
	reg := Discover(examplesRoot(t))

	for _, e := range reg.Errors() {
		t.Errorf("a shipped example does not parse: %v", e)
	}

	want := []string{"default", "git/status", "project/start", "system/cleanup", "system/doctor"}
	for _, id := range want {
		if _, ok := reg.Lookup(id); !ok {
			t.Errorf("example %q is missing", id)
		}
	}
	if reg.Len() != len(want) {
		t.Errorf("found %d examples, want %d: %v", reg.Len(), len(want), reg.IDs())
	}
}

// TestShippedExamplesExerciseTheLanguage stops the examples drifting into a
// narrow subset: between them they should use every construct the parser
// accepts, so that what they demonstrate matches what the language does.
func TestShippedExamplesExerciseTheLanguage(t *testing.T) {
	reg := Discover(examplesRoot(t))

	seen := map[string]bool{}
	for _, cmd := range reg.Commands() {
		prog := cmd.Program
		if prog.Instruction != "" {
			seen["instruction"] = true
		}
		if prog.Confirm != nil {
			seen["confirm"] = true
		}
		if len(prog.Args) > 0 {
			seen["args"] = true
		}
		scanBlock(prog.Run, seen)
	}

	for _, name := range []string{"instruction", "confirm", "args", "exec", "env", "fn", "let", "if"} {
		if !seen[name] {
			t.Errorf("no shipped example uses the %s construct", name)
		}
	}
}

func scanBlock(b *nscript.Block, seen map[string]bool) {
	if b == nil {
		return
	}
	for _, s := range b.Stmts {
		switch v := s.(type) {
		case *nscript.ExecStmt:
			seen["exec"] = true
		case *nscript.EnvStmt:
			seen["env"] = true
		case *nscript.LetStmt:
			seen["let"] = true
		case *nscript.FnStmt:
			seen["fn"] = true
			scanBlock(v.Body, seen)
		case *nscript.IfStmt:
			seen["if"] = true
			scanBlock(v.Then, seen)
			scanBlock(v.Else, seen)
		}
	}
}

// TestFallbackExampleIsUsableAsAFallback enforces the constraint the CLI
// documents: nothing selects a fallback, so every argument it declares must
// carry a default (D42).
func TestFallbackExampleIsUsableAsAFallback(t *testing.T) {
	reg := Discover(examplesRoot(t))

	fb, ok := reg.Lookup("default")
	if !ok {
		t.Fatal("the fallback example is missing")
	}
	for _, arg := range fb.Args() {
		if arg.Default == nil {
			t.Errorf("the fallback example declares %q with no default, so it could not run as a fallback (D42)", arg.Name)
		}
	}
}
