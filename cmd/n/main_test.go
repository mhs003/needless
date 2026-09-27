package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/testenv"
)

// The tests here run the real `n` binary rather than calling internal/cli. That
// covers the part the package tests cannot: that main wires the streams, the
// exit code and the signal handling together at all. They need no model, so
// they are fast and always run.

// buildN returns the compiled `n` binary, building it once per test binary.
func buildN(t *testing.T) string {
	t.Helper()
	return testenv.Binary(t, "./cmd/n", "n")
}

// nHome is a temporary HOME holding a command directory and nothing else.
func nHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".needless", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An editor must never launch during a test.
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	return home
}

func writeCommand(t *testing.T, home, id, body string) {
	t.Helper()
	path := filepath.Join(home, ".needless", "commands", filepath.FromSlash(id)+".nsc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runN executes the binary with a clean HOME and returns its exit code and
// streams. stdin is closed, so anything that prompts sees EOF and declines.
func runN(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(buildN(t), args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	cmd.Stdin = strings.NewReader("")

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return code, stdout.String(), stderr.String()
}

func TestBinaryHelp(t *testing.T) {
	home := nHome(t)

	code, out, _ := runN(t, home, "--help")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"Usage:", "--list-commands", "--remove", "--show-command", "Exit codes:"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output is missing %q", want)
		}
	}
}

func TestBinaryListCommands(t *testing.T) {
	home := nHome(t)
	writeCommand(t, home, "git/status", "instruction \"Show the git status.\"\nrun { print(\"x\") }\n")
	writeCommand(t, home, "say", "instruction \"Say something.\"\nrun { print(\"x\") }\n")

	code, out, errOut := runN(t, home, "--list-commands")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"git/status", "say"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q: %q", want, out)
		}
	}
}

func TestBinaryShowCommand(t *testing.T) {
	home := nHome(t)
	body := "instruction \"Say something.\"\nrun { print(\"x\") }\n"
	writeCommand(t, home, "say", body)

	code, out, _ := runN(t, home, "--show-command", "say")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out != body {
		t.Errorf("stdout = %q, want the file verbatim", out)
	}
}

// TestBinaryUnknownOption guards the usage exit code end to end: 2 is part of
// the CLI's contract (CLI spec §13), not an internal convention.
func TestBinaryUnknownOption(t *testing.T) {
	code, out, errOut := runN(t, nHome(t), "--wat")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "unknown option") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestBinaryNoArguments(t *testing.T) {
	code, _, errOut := runN(t, nHome(t))
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "no prompt") {
		t.Errorf("stderr = %q", errOut)
	}
}

// TestBinaryPromptWithNoCommandsReachesThePromptPath is the cheapest check that
// a prompt runs the whole way through the binary: it fails at "no commands
// found" rather than at argument parsing, and reports exit 1.
func TestBinaryPromptWithNoCommands(t *testing.T) {
	code, out, errOut := runN(t, nHome(t), "do something")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no commands found") {
		t.Errorf("stderr = %q", errOut)
	}
}

// TestBinaryRemoveDeclinesWithoutATerminal pins the safety property at the
// binary level: with stdin at EOF, `--remove` must not delete anything.
func TestBinaryRemoveDeclinesWithoutATerminal(t *testing.T) {
	home := nHome(t)
	writeCommand(t, home, "say", "instruction \"Say something.\"\nrun { print(\"x\") }\n")

	code, _, errOut := runN(t, home, "--remove", "say")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	path := filepath.Join(home, ".needless", "commands", "say.nsc")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was removed without confirmation: %v", err)
	}
	if !strings.Contains(errOut, "was not removed") {
		t.Errorf("stderr = %q", errOut)
	}
}
