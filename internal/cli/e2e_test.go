package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/testenv"
)

// Everything else in this package injects a fake model, which is what makes the
// suite fast and deterministic — but it also means the seam between the CLI and
// the real engine is never exercised. These tests close that gap. They are slow
// (the model is 34 MiB), need artifacts that are not committed, and are gated by
// testenv.RequireRealModel and testing.Short.

const weatherCmd = `instruction """
Report the current weather for a city.

Use this command when the user asks about the weather anywhere.
"""

confirm false

args {
    city: string
}

run {
    env {
        CITY = city
    }

    exec bash <<(
echo "weather for $CITY"
)<<
}
`

const echoCmd = `instruction """
Repeat a message back to the user.

Use this command when the user asks to echo or repeat something.
"""

confirm false

args {
    message: string
}

run {
    print("echo: " + message)
}
`

// e2eHome builds a temporary HOME wired to the real model and the real worker.
//
// It deliberately does not use setupHome: that installs a placeholder archive
// so the fake-model tests can resolve a path, and a real run must not find it.
func e2eHome(t *testing.T, model string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	// Point the worker at the freshly built binary rather than relying on where
	// the test binary happens to live.
	t.Setenv("NEEDLE_WORKER", testenv.Binary(t, "./needle/cmd/needle-worker", "needle-worker"))

	// The CLI does not honour NEEDLE_MODEL, so the archive is named explicitly.
	writeConfig(t, home, fmt.Sprintf(`{"weights": %q}`, model))
	return home
}

// runE2E drives the real CLI, including its real model factory, with stdin at
// EOF so nothing can block on a prompt.
func runE2E(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestE2ERealModelRunsTheMatchedCommand is the one check nothing else makes: a
// real prompt, through the real parser, the real engine and the real worker,
// ending in a command's actual output.
//
// Two commands are installed and the prompt suits one of them, so a match also
// proves the selection discriminated rather than merely executing whatever it
// found first.
func TestE2ERealModelRunsTheMatchedCommand(t *testing.T) {
	model := testenv.RequireRealModel(t)
	home := e2eHome(t, model)
	writeCommand(t, home, "weather", weatherCmd)
	writeCommand(t, home, "echo", echoCmd)

	code, out, errOut := runE2E(t, "what is the weather in Paris")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}

	lower := strings.ToLower(out)
	if !strings.Contains(lower, "weather for") {
		t.Fatalf("stdout = %q, want the weather command's output", out)
	}
	// The city arrived through the env block, which is the only way nscript
	// passes values to an embedded script.
	if !strings.Contains(lower, "paris") {
		t.Errorf("stdout = %q, want the city the prompt named", out)
	}
	// The other installed command must not have run.
	if strings.Contains(lower, "echo:") {
		t.Errorf("stdout = %q, but the echo command should not have run", out)
	}
}

// TestE2ERealModelReportsAnUnfillableArgument is B1 end to end. The prompt names
// no city, so the model has nothing to put in the slot; the command must not run
// with an empty value, and the CLI must say which value was missing.
func TestE2ERealModelReportsAnUnfillableArgument(t *testing.T) {
	model := testenv.RequireRealModel(t)
	home := e2eHome(t, model)
	writeCommand(t, home, "weather", weatherCmd)

	code, out, errOut := runE2E(t, "show me the weather")
	if code == ExitOK {
		t.Fatalf("exit = 0, but nothing should have run: stdout = %q", out)
	}
	// It must not have run the script with an empty $CITY, which was the whole
	// point of the fix.
	if strings.Contains(out, "weather for") {
		t.Errorf("stdout = %q, want the run block not to have executed", out)
	}
	if errOut == "" {
		t.Error("stderr is empty; the user was told nothing")
	}
}

// TestE2EHomeListsItsCommands is the cheap half of the pair: no model needed, so
// it fails fast when the wiring above is broken rather than at the end of a
// model load.
func TestE2EHomeListsItsCommands(t *testing.T) {
	model := testenv.RealModel(t)
	if model == "" {
		model = "/nonexistent/needle3.cact"
	}
	home := e2eHome(t, model)
	writeCommand(t, home, "weather", weatherCmd)
	writeCommand(t, home, "git/status", "instruction \"Show the git status.\"\nrun { print(\"x\") }\n")

	code, out, errOut := runE2E(t, "--list-commands")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"weather", "git/status"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q: %q", want, out)
		}
	}
}

// TestE2ERealModelChoosesFromManyCommands exercises the retrieval head, which
// every other test bypasses: with a handful of commands the engine renders the
// whole toolset, and with TopicCount it has to choose.
//
// It asserts only that the command which ran is one of the declared ones. Which
// command the model prefers is a quality question, not a correctness one, and
// asserting a specific answer here would make the suite flaky; the choice is
// logged instead so a human reading -v output can see it.
func TestE2ERealModelChoosesFromManyCommands(t *testing.T) {
	model := testenv.RequireRealModel(t)
	home := e2eHome(t, model)

	declared := make(map[string]bool, testenv.TopicCount)
	for i := 0; i < testenv.TopicCount; i++ {
		id, src := testenv.ScaleCommand(i)
		writeCommand(t, home, id, src)
		declared[id] = true
	}

	// The prompt quotes one command's instruction almost verbatim, so a good
	// answer is obvious to a reader even though it is not asserted.
	code, out, errOut := runE2E(t, "check the weather subsystem and report its status")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}

	ran, ok := strings.CutPrefix(strings.TrimSpace(out), "ran:")
	if !ok {
		t.Fatalf("stdout = %q, want a command's marker", out)
	}
	if !declared[ran] {
		t.Fatalf("ran %q, which is not one of the %d declared commands", ran, len(declared))
	}

	if ran == "gen/weather" {
		t.Logf("with %d commands installed, the prompt chose %q as expected", len(declared), ran)
	} else {
		t.Logf("with %d commands installed, the prompt chose %q; gen/weather would have been the exact match",
			len(declared), ran)
	}
}
