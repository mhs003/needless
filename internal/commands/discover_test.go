package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// writeCmd creates a .nsc file at root/rel, creating parent directories.
func writeCmd(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// script builds a minimal valid command with the given instruction.
func script(instruction string) string {
	return "instruction " + strconv.Quote(instruction) + "\nrun { print(\"ok\") }\n"
}

func ids(reg *Registry) []string { return reg.IDs() }

func TestDiscoverNestedLayout(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "project/start.nsc", script("Start the development server for a project."))
	writeCmd(t, root, "project/stop.nsc", script("Stop the development server."))
	writeCmd(t, root, "git/status.nsc", script("Show the current Git status."))
	writeCmd(t, root, "system/cleanup.nsc", script("Clean temporary system files."))

	reg := Discover(root)

	if errs := reg.Errors(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []string{"git/status", "project/start", "project/stop", "system/cleanup"}
	if got := ids(reg); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestDiscoverParsesTheScript(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "deploy.nsc", `
instruction """
Deploy the application.
"""

args {
    environment: string
    force: bool = false
}

confirm true

run {
    print("deploying")
}
`)
	reg := Discover(root)
	if reg.Len() != 1 {
		t.Fatalf("len = %d, want 1 (errors: %v)", reg.Len(), reg.Errors())
	}

	cmd, ok := reg.Lookup("deploy")
	if !ok {
		t.Fatal("Lookup(deploy) failed")
	}
	if got, want := cmd.Instruction(), "Deploy the application."; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
	args := cmd.Args()
	if len(args) != 2 || args[0].Name != "environment" || args[1].Name != "force" {
		t.Errorf("args = %+v", args)
	}
	if cmd.Program.Confirm == nil || *cmd.Program.Confirm != true {
		t.Errorf("confirm = %v, want true", cmd.Program.Confirm)
	}
	if cmd.String() != "deploy" {
		t.Errorf("String() = %q", cmd.String())
	}
	if !strings.HasSuffix(cmd.Path, "deploy.nsc") {
		t.Errorf("Path = %q", cmd.Path)
	}
}

func TestDiscoverIDUsesForwardSlashes(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "a/b/c/deep.nsc", script("Deeply nested."))

	reg := Discover(root)
	if got := ids(reg); len(got) != 1 || got[0] != "a/b/c/deep" {
		t.Fatalf("ids = %v, want [a/b/c/deep]", got)
	}
	if strings.ContainsAny(ids(reg)[0], `\`) {
		t.Fatalf("id %q should not contain a backslash", ids(reg)[0])
	}
}

func TestDiscoverSkipsNonNscAndHidden(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "real.nsc", script("A real command."))
	writeCmd(t, root, "notes.txt", "not a command")
	writeCmd(t, root, "README.md", "# nope")
	writeCmd(t, root, ".hidden.nsc", script("Hidden file."))
	writeCmd(t, root, ".git/config.nsc", script("Inside a hidden directory."))
	writeCmd(t, root, "backup.nsc.bak", "leftover")

	reg := Discover(root)
	if got := ids(reg); !reflect.DeepEqual(got, []string{"real"}) {
		t.Fatalf("ids = %v, want [real]", got)
	}
}

func TestDiscoverMissingRootIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	reg := Discover(missing)

	if reg.Len() != 0 {
		t.Fatalf("len = %d, want 0", reg.Len())
	}
	if errs := reg.Errors(); len(errs) != 0 {
		t.Fatalf("a missing root should not be an error, got %v", errs)
	}
}

func TestDiscoverNoRoots(t *testing.T) {
	reg := Discover()
	if reg.Len() != 0 || len(reg.Errors()) != 0 {
		t.Fatalf("empty Discover() = %d commands, %d errors", reg.Len(), len(reg.Errors()))
	}
	if got := reg.Commands(); len(got) != 0 {
		t.Fatalf("Commands() = %v", got)
	}
}

func TestDiscoverBlankRootIsIgnored(t *testing.T) {
	reg := Discover("", "   ")
	if reg.Len() != 0 || len(reg.Errors()) != 0 {
		t.Fatalf("got %d commands, %d errors", reg.Len(), len(reg.Errors()))
	}
}

func TestDiscoverEmptyRootDirectory(t *testing.T) {
	reg := Discover(t.TempDir())
	if reg.Len() != 0 || len(reg.Errors()) != 0 {
		t.Fatalf("got %d commands, %d errors", reg.Len(), len(reg.Errors()))
	}
}

func TestDiscoverKeepsGoodCommandsWhenOneIsBroken(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "good.nsc", script("A fine command."))
	writeCmd(t, root, "broken.nsc", "instruction \"unterminated\nrun { print(\"x\") }\n")
	writeCmd(t, root, "also-good.nsc", script("Another fine command."))

	reg := Discover(root)

	if got := ids(reg); !reflect.DeepEqual(got, []string{"also-good", "good"}) {
		t.Fatalf("ids = %v, want the two valid commands", got)
	}
	errs := reg.Errors()
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if !strings.HasSuffix(errs[0].Path, "broken.nsc") {
		t.Errorf("error path = %q", errs[0].Path)
	}
	if !strings.Contains(errs[0].Error(), "unterminated") {
		t.Errorf("error = %v, want it to describe the syntax problem", errs[0])
	}
	if errs[0].Unwrap() == nil {
		t.Error("LoadError should unwrap to the underlying error")
	}
}

func TestDiscoverFirstRootWins(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeCmd(t, first, "shared.nsc", script("The first definition."))
	writeCmd(t, second, "shared.nsc", script("The second definition."))
	writeCmd(t, second, "only-second.nsc", script("Unique to the second root."))

	reg := Discover(first, second)

	if got := ids(reg); !reflect.DeepEqual(got, []string{"only-second", "shared"}) {
		t.Fatalf("ids = %v", got)
	}
	cmd, _ := reg.Lookup("shared")
	if got, want := cmd.Instruction(), "The first definition."; got != want {
		t.Fatalf("instruction = %q, want the first root to win (%q)", got, want)
	}
}

func TestDiscoverOrderIsStableAcrossRuns(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zebra", "alpha", "mango", "beta"} {
		writeCmd(t, root, name+".nsc", script("Command "+name+"."))
	}

	first := ids(Discover(root))
	for i := 0; i < 5; i++ {
		again := ids(Discover(root))
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("order changed between runs: %v vs %v", first, again)
		}
	}
	want := []string{"alpha", "beta", "mango", "zebra"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("ids = %v, want %v", first, want)
	}
}

func TestRegistryLookup(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "git/status.nsc", script("Show the current Git status."))

	reg := Discover(root)

	if cmd, ok := reg.Lookup("git/status"); !ok || cmd.ID != "git/status" {
		t.Fatalf("Lookup hit = %v %v", cmd.ID, ok)
	}
	if _, ok := reg.Lookup("git"); ok {
		t.Error("Lookup(git) should miss: the ID has no extension and includes the directory")
	}
	if _, ok := reg.Lookup("status"); ok {
		t.Error("Lookup(status) should miss: bare filenames are not IDs")
	}
	if _, ok := reg.Lookup(""); ok {
		t.Error("Lookup(\"\") should miss")
	}
}

func TestRegistryCommandsIsACopy(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "a.nsc", script("A."))

	reg := Discover(root)
	got := reg.Commands()
	got[0].ID = "mutated"

	if again := reg.Commands(); again[0].ID == "mutated" {
		t.Fatal("Commands() exposed the registry's backing array")
	}
	if errs := reg.Errors(); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
}

func TestDiscoverRootThatIsAFile(t *testing.T) {
	dir := t.TempDir()
	path := writeCmd(t, dir, "solo.nsc", script("A lone command."))

	// Pointing a root at the file itself is unusual but should not panic.
	reg := Discover(path)
	if reg.Len() > 1 {
		t.Fatalf("len = %d, want at most 1", reg.Len())
	}
}

func TestDiscoverUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; file permissions are not enforced")
	}
	root := t.TempDir()
	path := writeCmd(t, root, "secret.nsc", script("A command."))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	reg := Discover(root)
	if reg.Len() != 0 {
		t.Fatalf("len = %d, want 0", reg.Len())
	}
	errs := reg.Errors()
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want one", errs)
	}
	if !os.IsPermission(errs[0].Err) {
		t.Errorf("error = %v, want a permission error", errs[0])
	}
}

func TestDiscoverReportsEveryBrokenCommand(t *testing.T) {
	root := t.TempDir()
	writeCmd(t, root, "one.nsc", "run { print(\"x\") }\n") // missing instruction
	writeCmd(t, root, "two.nsc", "instruction \"x\"\n")    // missing run
	writeCmd(t, root, "three.nsc", script("Valid one."))

	reg := Discover(root)
	if reg.Len() != 1 || ids(reg)[0] != "three" {
		t.Fatalf("ids = %v, want [three]", ids(reg))
	}
	if len(reg.Errors()) != 2 {
		t.Fatalf("errors = %v, want 2", reg.Errors())
	}
}
