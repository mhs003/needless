package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/config"
	"github.com/mhs003/needless/internal/intent"
	"github.com/mhs003/needless/internal/nscript"
	"github.com/mhs003/needless/needle"
)

// --- harness -------------------------------------------------------------

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fakeModel stands in for the 34 MiB model.
type fakeModel struct {
	reply needle.Result
	err   error

	calls   int
	gotConf needle.Config
}

func (f *fakeModel) CompleteResult(context.Context, string, int) (needle.Result, error) {
	f.calls++
	if f.err != nil {
		return needle.Result{}, f.err
	}
	return f.reply, nil
}

func fakeFactory(m *fakeModel) modelFactory {
	return func(_ context.Context, _ needle.Config) (intent.Completer, io.Closer, error) {
		return m, nopCloser{}, nil
	}
}

// setupHome points HOME at a temp directory so tests never touch the real one,
// and places a placeholder model archive so path resolution succeeds. Its
// contents are never read: the model itself is faked.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// An editor must never actually launch during tests.
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	models := filepath.Join(home, ".needless", "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(models, "needle3.cact"), []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func commandsRoot(home string) string {
	return filepath.Join(home, ".needless", "commands")
}

func writeCommand(t *testing.T, home, id, src string) string {
	t.Helper()
	path := filepath.Join(commandsRoot(home), filepath.FromSlash(id)+".nsc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".needless")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run drives the CLI with a fake model and returns the exit code and streams.
func run(t *testing.T, m *fakeModel, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{
		stdin:  strings.NewReader(stdin),
		stdout: &out,
		stderr: &errOut,
	}
	if m != nil {
		a.newModel = fakeFactory(m)
	}
	code := a.main(context.Background(), args)
	return code, out.String(), errOut.String()
}

const listCmd = `instruction """
List a project's files.
"""

args {
    project: string
}

run {
    print("listing " + project)
}
`

func matchedReply(name string, args map[string]any) needle.Result {
	raw, _ := json.Marshal(args)
	return needle.Result{
		Type:          "call",
		Success:       true,
		FunctionCalls: []needle.FunctionCall{{Name: name, Arguments: raw}},
	}
}

func refusedReply() needle.Result {
	return needle.Result{Type: "respond", Success: true, Reasoning: "nothing fits"}
}

// --- argument parsing ----------------------------------------------------

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    options
		wantErr bool
	}{
		{name: "help long", args: []string{"--help"}, want: options{mode: modeHelp}},
		{name: "help short", args: []string{"-h"}, want: options{mode: modeHelp}},
		{name: "list", args: []string{"--list-commands"}, want: options{mode: modeList}},
		{name: "list ls", args: []string{"--ls"}, want: options{mode: modeList}},
		{name: "list short", args: []string{"-l"}, want: options{mode: modeList}},
		{name: "new without id", args: []string{"--new"}, want: options{mode: modeNew}},
		{name: "new with id", args: []string{"--new", "git/status"}, want: options{mode: modeNew, operand: "git/status"}},
		{name: "edit without id", args: []string{"-e"}, want: options{mode: modeEdit}},
		{name: "edit with id", args: []string{"--edit", "git/status"}, want: options{mode: modeEdit, operand: "git/status"}},
		{name: "remove without id", args: []string{"--remove"}, want: options{mode: modeRemove}},
		{name: "remove with id", args: []string{"--remove", "git/status"}, want: options{mode: modeRemove, operand: "git/status"}},
		{name: "show without id", args: []string{"--show-command"}, want: options{mode: modeShow}},
		{name: "show with id", args: []string{"--show-command", "a/b"}, want: options{mode: modeShow, operand: "a/b"}},
		{name: "show alias", args: []string{"--show", "a/b"}, want: options{mode: modeShow, operand: "a/b"}},

		{name: "bare prompt", args: []string{"run", "the", "project"}, want: options{prompt: "run the project"}},
		{name: "quoted prompt", args: []string{"run the project"}, want: options{prompt: "run the project"}},
		{name: "empty", args: nil, want: options{}},
		{name: "double dash ends options", args: []string{"--", "--not-a-flag"}, want: options{prompt: "--not-a-flag"}},
		{name: "single dash is a word", args: []string{"-"}, want: options{prompt: "-"}},

		{name: "unknown option", args: []string{"--nope"}, wantErr: true},
		{name: "unknown short", args: []string{"-z"}, wantErr: true},
		{name: "extra after help", args: []string{"--help", "now"}, wantErr: true},
		{name: "extra after list", args: []string{"--list-commands", "now"}, wantErr: true},
		{name: "extra after remove", args: []string{"--remove", "a", "b"}, wantErr: true},
		{name: "extra after show", args: []string{"--show-command", "a", "b"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("parseArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

// --- top level -----------------------------------------------------------

func TestMainHelp(t *testing.T) {
	setupHome(t)
	code, out, _ := run(t, nil, "", "--help")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	for _, want := range []string{"Usage:", "--list-commands", "--new-command", "--edit-command", "Exit codes:"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

func TestMainUnknownOption(t *testing.T) {
	setupHome(t)
	code, out, errOut := run(t, nil, "", "--wat")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "unknown option") || !strings.Contains(errOut, "--help") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainNoArguments(t *testing.T) {
	setupHome(t)
	code, _, errOut := run(t, nil, "")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "no prompt") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainPromptWithNoCommands(t *testing.T) {
	setupHome(t)
	code, _, errOut := run(t, &fakeModel{}, "", "run the project")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "no commands found") {
		t.Errorf("stderr = %q", errOut)
	}
}

// --- list ----------------------------------------------------------------

func TestMainListCommands(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "git/status", `
instruction """
Show the current Git status.
"""
run { print("status") }
`)
	writeCommand(t, home, "project/start", `
instruction """
Start the development server for a project.
"""
run { print("start") }
`)

	code, out, _ := run(t, nil, "", "--list-commands")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out, "git/status") || !strings.Contains(out, "project/start") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(out, "Show the current Git status.") {
		t.Errorf("instruction missing from the listing: %q", out)
	}
	// Sorted by id.
	if strings.Index(out, "git/status") > strings.Index(out, "project/start") {
		t.Errorf("listing is not sorted: %q", out)
	}
}

func TestMainListWithNoCommandsIsSuccessful(t *testing.T) {
	setupHome(t)
	code, out, errOut := run(t, nil, "", "--list-commands")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (an empty listing is a successful query)", code, ExitOK)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no commands found") {
		t.Errorf("stderr = %q, want a hint", errOut)
	}
}

func TestMainReportsUnparsableCommands(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "good", listCmd)
	writeCommand(t, home, "broken", "instruction \"unterminated\nrun { print(\"x\") }\n")

	code, out, errOut := run(t, nil, "", "--list-commands")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "good") {
		t.Errorf("the good command should still be listed: %q", out)
	}
	if !strings.Contains(errOut, "skipping") || !strings.Contains(errOut, "broken.nsc") {
		t.Errorf("stderr = %q, want a warning naming the broken file", errOut)
	}
}

// --- new -----------------------------------------------------------------

func TestMainNewCommandCreatesAValidTemplate(t *testing.T) {
	home := setupHome(t)
	code, _, errOut := run(t, nil, "", "--new-command", "git/status")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}

	path := filepath.Join(commandsRoot(home), "git", "status.nsc")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	// A fresh file must parse, or `n --list-commands` would warn about it.
	if _, err := nscript.Parse(body); err != nil {
		t.Fatalf("the created template does not parse: %v\n%s", err, body)
	}
	if !strings.Contains(errOut, "Created") {
		t.Errorf("stderr = %q, want confirmation", errOut)
	}
}

func TestMainNewCommandAcceptsTheExtension(t *testing.T) {
	home := setupHome(t)
	code, _, _ := run(t, nil, "", "--new", "thing.nsc")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(commandsRoot(home), "thing.nsc")); err != nil {
		t.Fatalf("expected thing.nsc, got %v", err)
	}
}

func TestMainNewCommandPromptsForAnID(t *testing.T) {
	home := setupHome(t)
	code, _, _ := run(t, nil, "deploy\n", "--new-command")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(commandsRoot(home), "deploy.nsc")); err != nil {
		t.Fatalf("expected deploy.nsc, got %v", err)
	}
}

func TestMainNewCommandRefusesToOverwrite(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "thing", listCmd)

	code, _, errOut := run(t, nil, "", "--new-command", "thing")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "already exists") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainNewCommandRejectsBadIDs(t *testing.T) {
	setupHome(t)
	for _, id := range []string{"/abs", "../up", "a//b", "with space", "a/../b"} {
		code, _, errOut := run(t, nil, "", "--new-command", id)
		if code != ExitUsage {
			t.Errorf("id %q: exit = %d, want %d", id, code, ExitUsage)
		}
		if errOut == "" {
			t.Errorf("id %q: expected an explanation", id)
		}
	}
}

func TestMainNewCommandRunsTheEditor(t *testing.T) {
	setupHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("EDITOR", "touch "+marker)

	code, _, _ := run(t, nil, "", "--new-command", "edited")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("$EDITOR was not invoked: %v", err)
	}
}

// --- edit ----------------------------------------------------------------

func TestMainEditWithoutEditorPrintsThePath(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "git/status", listCmd)

	code, _, errOut := run(t, nil, "", "--edit-command", "git/status")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr = %q", code, ExitOK, errOut)
	}
	if !strings.Contains(errOut, "status.nsc") {
		t.Errorf("stderr = %q, want the file path", errOut)
	}
	if !strings.Contains(errOut, "$EDITOR") {
		t.Errorf("stderr = %q, want a hint about $EDITOR", errOut)
	}
}

func TestMainEditUnknownCommand(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "thing", listCmd)

	code, _, errOut := run(t, nil, "", "--edit-command", "nope")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "no command named") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainEditInteractiveSelection(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "alpha", listCmd)
	writeCommand(t, home, "beta", listCmd)

	code, _, errOut := run(t, nil, "2\n", "--edit-command")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr = %q", code, errOut)
	}
	if !strings.Contains(errOut, "beta.nsc") {
		t.Errorf("stderr = %q, want the second command's path", errOut)
	}
}

func TestMainEditInteractiveBadChoice(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "alpha", listCmd)

	code, _, _ := run(t, nil, "9\n", "--edit-command")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
}

func TestMainEditWithNoCommands(t *testing.T) {
	setupHome(t)
	code, _, errOut := run(t, nil, "", "--edit-command")
	if code != ExitFailure {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, "no commands to edit") {
		t.Errorf("stderr = %q", errOut)
	}
}

// --- show ----------------------------------------------------------------

func TestMainShowCommandPrintsTheSource(t *testing.T) {
	home := setupHome(t)
	src := listCmd
	path := writeCommand(t, home, "list", src)

	code, out, errOut := run(t, nil, "", "--show-command", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	// The source is the requested output, so it is on stdout, byte for byte.
	if out != src {
		t.Fatalf("stdout = %q, want the file contents verbatim", out)
	}
	// The path is a diagnostic, so it is on stderr and does not pollute a pipe.
	if !strings.Contains(errOut, path) {
		t.Errorf("stderr = %q, want the file path", errOut)
	}
}

func TestMainShowCommandAcceptsTheExtension(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	code, out, _ := run(t, nil, "", "--show", "list.nsc")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if out != listCmd {
		t.Errorf("stdout = %q", out)
	}
}

func TestMainShowCommandUnknown(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	code, out, errOut := run(t, nil, "", "--show-command", "ghost")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no command named") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainShowCommandSelectsInteractively(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "alpha", listCmd)
	writeCommand(t, home, "beta", `instruction "beta one"
run { print("beta") }
`)

	code, out, errOut := run(t, nil, "2\n", "--show-command")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, `instruction "beta one"`) {
		t.Errorf("stdout = %q, want the second command's source", out)
	}
	if !strings.Contains(errOut, "beta.nsc") {
		t.Errorf("stderr = %q, want the path", errOut)
	}
}

// --- remove --------------------------------------------------------------

func TestMainRemoveCommand(t *testing.T) {
	home := setupHome(t)
	path := writeCommand(t, home, "list", listCmd)

	code, out, errOut := run(t, nil, "y\n", "--remove", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file still exists: %v", err)
	}
	if !strings.Contains(errOut, "Remove this command?") {
		t.Errorf("stderr = %q, want it to have asked first", errOut)
	}
	if !strings.Contains(errOut, "Removed list") {
		t.Errorf("stderr = %q", errOut)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestMainRemoveDeclinedKeepsTheFile(t *testing.T) {
	home := setupHome(t)
	path := writeCommand(t, home, "list", listCmd)

	// An empty answer and EOF (no terminal) must both decline.
	for _, answer := range []string{"\n", "", "n\n", "maybe\n"} {
		code, _, errOut := run(t, nil, answer, "--remove", "list")
		if code != ExitFailure {
			t.Errorf("answer %q: exit = %d, want %d", answer, code, ExitFailure)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("answer %q: the file was removed: %v", answer, err)
		}
		if !strings.Contains(errOut, "was not removed") {
			t.Errorf("answer %q: stderr = %q", answer, errOut)
		}
	}
}

func TestMainRemoveUnknownCommand(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	code, _, errOut := run(t, nil, "y\n", "--remove", "ghost")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "no command named") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainRemoveSelectsInteractively(t *testing.T) {
	home := setupHome(t)
	alpha := writeCommand(t, home, "alpha", listCmd)
	beta := writeCommand(t, home, "beta", listCmd)

	code, _, errOut := run(t, nil, "1\ny\n", "--remove")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if _, err := os.Stat(alpha); !os.IsNotExist(err) {
		t.Errorf("alpha should be gone: %v", err)
	}
	if _, err := os.Stat(beta); err != nil {
		t.Errorf("beta should still exist: %v", err)
	}
}

func TestMainRemoveWithNoCommands(t *testing.T) {
	setupHome(t)
	code, _, errOut := run(t, nil, "", "--remove")
	if code != ExitFailure {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, "no commands to remove") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainRemoveBadChoice(t *testing.T) {
	home := setupHome(t)
	path := writeCommand(t, home, "alpha", listCmd)

	code, _, _ := run(t, nil, "9\n", "--remove")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a bad choice must not remove anything: %v", err)
	}
}

// TestWithinRoots pins the check that guards deletion. Discovery only ever
// yields paths under a root, so this is defence against a future change or an
// odd configuration — which is exactly why it is tested directly rather than
// through the CLI.
func TestWithinRoots(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "home", "u", ".needless", "commands")
	other := filepath.Join(string(filepath.Separator), "home", "u", "elsewhere")

	cases := []struct {
		name string
		path string
		want bool
	}{
		{name: "a command directly under the root", path: filepath.Join(root, "list.nsc"), want: true},
		{name: "a command in a subdirectory", path: filepath.Join(root, "git", "status.nsc"), want: true},
		{name: "the root itself", path: root, want: false},
		{name: "a parent of the root", path: filepath.Dir(root), want: false},
		{name: "outside every root", path: filepath.Join(other, "list.nsc"), want: false},
		{name: "a sibling sharing a prefix", path: root + "-backup/x.nsc", want: false},
		{name: "an unnormalised path inside", path: filepath.Join(root, "a", "..", "b.nsc"), want: true},
		{name: "an unnormalised path escaping", path: filepath.Join(root, "..", "outside.nsc"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinRoots(tc.path, []string{root}); got != tc.want {
				t.Errorf("withinRoots(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	// A path under the second root is still inside the roots.
	second := filepath.Join(string(filepath.Separator), "opt", "shared")
	if !withinRoots(filepath.Join(second, "x.nsc"), []string{root, second}) {
		t.Error("a path under any configured root should be allowed")
	}
}

// --- prompt flow ---------------------------------------------------------

func TestMainPromptRunsTheMatchedCommand(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	m := &fakeModel{reply: matchedReply("list", map[string]any{"project": "shop"})}
	code, out, errOut := run(t, m, "", "list", "the", "shop", "project")

	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "listing shop\n" {
		t.Fatalf("stdout = %q, want the command's own output", out)
	}
	if m.calls != 1 {
		t.Errorf("model called %d times, want 1", m.calls)
	}
}

func TestMainPromptBuildsTheToolsetFromCommands(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	m := &fakeModel{reply: matchedReply("list", map[string]any{"project": "x"})}
	var got needle.Config
	var out, errOut bytes.Buffer
	a := &app{
		stdin:  strings.NewReader(""),
		stdout: &out,
		stderr: &errOut,
		newModel: func(_ context.Context, cfg needle.Config) (intent.Completer, io.Closer, error) {
			got = cfg
			return m, nopCloser{}, nil
		},
	}
	if code := a.main(context.Background(), []string{"list x"}); code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	if !strings.Contains(got.ToolsJSON, `"name":"list"`) {
		t.Errorf("ToolsJSON = %q, want the command in it", got.ToolsJSON)
	}
	if !strings.Contains(got.ToolsJSON, "List a project's files.") {
		t.Errorf("ToolsJSON = %q, want the instruction as the description", got.ToolsJSON)
	}
	// A date fact is supplied when the configuration names none (D44).
	if !strings.HasPrefix(got.SystemPrompt, "date: ") {
		t.Errorf("SystemPrompt = %q, want a date fact", got.SystemPrompt)
	}
	if got.WeightsPath == "" {
		t.Error("WeightsPath is empty")
	}
}

func TestMainPromptRefusalWithNoFallback(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	m := &fakeModel{reply: refusedReply()}
	code, out, errOut := run(t, m, "", "what is the capital of France")

	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "no command matched") {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(errOut, "no fallback") {
		t.Errorf("stderr = %q, want the missing fallback mentioned", errOut)
	}
}

// TestMainPromptReportsAnUnfilledArgument is the CLI half of the B1 fix. A
// command that was recognised but had no value for a required argument must be
// named as such, and must not run with a hole where the value goes.
func TestMainPromptReportsAnUnfilledArgument(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "start", `instruction """
Start the development server for a project.
"""
args {
    project: string
}
run {
    print("STARTED " + project)
}
`)

	m := &fakeModel{reply: matchedReply("start", map[string]any{"project": ""})}
	code, out, errOut := run(t, m, "", "start the server")

	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if out != "" {
		t.Fatalf("the run block must not execute with an empty required argument: stdout = %q", out)
	}
	if !strings.Contains(errOut, "start needs a value for project") {
		t.Errorf("stderr = %q, want it to name the missing value", errOut)
	}
	if strings.Contains(errOut, "no command matched") {
		t.Errorf("stderr = %q, but the command was matched — it was only unfillable", errOut)
	}
}

func TestMainPromptUnfilledArgumentUsesTheFallback(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "start", `instruction """
Start the development server for a project.
"""
args {
    project: string
}
run {
    print("STARTED " + project)
}
`)
	writeCommand(t, home, "default", `instruction """
Handle a request nothing else could.
"""
args {
    note: string = "nothing"
}
run { print("fallback: " + note) }
`)
	writeConfig(t, home, `{"fallback": "default"}`)

	m := &fakeModel{reply: matchedReply("start", map[string]any{"project": ""})}
	code, out, errOut := run(t, m, "", "start the server")

	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "fallback: nothing") {
		t.Fatalf("stdout = %q, want the fallback to have run", out)
	}
	if strings.Contains(out, "STARTED") {
		t.Errorf("the unfillable command ran anyway: stdout = %q", out)
	}
	if !strings.Contains(errOut, "needs a value for project") {
		t.Errorf("stderr = %q, want the reason on stderr even though it fell back", errOut)
	}
}

func TestMainPromptUsesTheFallback(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)
	writeCommand(t, home, "default", `
instruction """
Fall back to this when nothing else fits.
"""
args {
    note: string = "nothing"
}
run { print("fallback ran: " + note) }
`)
	writeConfig(t, home, `{"fallback": "default"}`)

	m := &fakeModel{reply: refusedReply()}
	code, out, errOut := run(t, m, "", "something unmatched")

	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "fallback ran: nothing") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "falling back") {
		t.Errorf("stderr = %q, want it to say what happened", errOut)
	}
}

func TestMainPromptFallbackNeedsDefaults(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)
	writeCommand(t, home, "default", `
instruction """
Fall back.
"""
args {
    required: string
}
run { print(required) }
`)
	writeConfig(t, home, `{"fallback": "default"}`)

	m := &fakeModel{reply: refusedReply()}
	code, _, errOut := run(t, m, "", "something unmatched")

	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "cannot be used as a fallback") {
		t.Errorf("stderr = %q, want an explanation about defaults", errOut)
	}
}

func TestMainPromptUnknownFallback(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)
	writeConfig(t, home, `{"fallback": "ghost"}`)

	m := &fakeModel{reply: refusedReply()}
	code, _, errOut := run(t, m, "", "something unmatched")

	if code != ExitFailure {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, `"ghost" is not a known command`) {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainPromptReportsAWithheldCall(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	args, _ := json.Marshal(map[string]any{"project": "x"})
	m := &fakeModel{reply: needle.Result{
		Type:            "call",
		SuppressedCalls: []needle.FunctionCall{{Name: "list", Arguments: args}},
	}}
	code, out, errOut := run(t, m, "", "maybe list something")

	if code != ExitFailure {
		t.Fatalf("exit = %d", code)
	}
	if out != "" {
		t.Errorf("a withheld call must not run: stdout = %q", out)
	}
	if !strings.Contains(errOut, "withheld") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainPromptModelFailure(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "list", listCmd)

	m := &fakeModel{err: errors.New("engine exploded")}
	code, _, errOut := run(t, m, "", "list something")

	if code != ExitFailure {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, "engine exploded") {
		t.Errorf("stderr = %q", errOut)
	}
}

// --- confirmation --------------------------------------------------------

const confirmCmd = `instruction """
Remove the generated build files.
"""

confirm true

args {
    project: string
}

run {
    print("removing " + project)
}
`

func TestMainPromptConfirmationAccepted(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "clean", confirmCmd)

	m := &fakeModel{reply: matchedReply("clean", map[string]any{"project": "shop"})}
	code, out, errOut := run(t, m, "y\n", "clean the shop")

	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "removing shop\n" {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "Confirmation required.") {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(errOut, "Continue?") {
		t.Errorf("stderr = %q, want the prompt", errOut)
	}
}

func TestMainPromptConfirmationDeclined(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "clean", confirmCmd)

	m := &fakeModel{reply: matchedReply("clean", map[string]any{"project": "shop"})}
	code, out, errOut := run(t, m, "n\n", "clean the shop")

	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if out != "" {
		t.Fatalf("the run block must not execute: stdout = %q", out)
	}
	if !strings.Contains(errOut, "aborted") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainPromptConfirmationDefaultsToNo(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "clean", confirmCmd)

	m := &fakeModel{reply: matchedReply("clean", map[string]any{"project": "shop"})}
	// An empty reply, and stdin at EOF, must both decline.
	for _, stdin := range []string{"\n", "", "maybe\n"} {
		code, out, _ := run(t, m, stdin, "clean the shop")
		if code != ExitFailure {
			t.Errorf("stdin %q: exit = %d, want %d", stdin, code, ExitFailure)
		}
		if out != "" {
			t.Errorf("stdin %q: stdout = %q, want nothing", stdin, out)
		}
	}
}

func TestMainPromptConfirmationDoesNotEchoArguments(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "secret", `instruction """
Change a password.
"""
confirm true
args {
    password: string
}
run { print("changed") }
`)

	m := &fakeModel{reply: matchedReply("secret", map[string]any{"password": "hunter2"})}
	_, _, errOut := run(t, m, "y\n", "change the password")

	if strings.Contains(errOut, "hunter2") {
		t.Errorf("a sensitive argument was echoed in the confirmation: %q", errOut)
	}
}

// --- execution -----------------------------------------------------------

func TestMainPromptCommandFailureExitsOne(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "boom", `instruction """
Fail on purpose.
"""
run {
    error("it broke")
}
`)

	m := &fakeModel{reply: matchedReply("boom", map[string]any{})}
	code, _, errOut := run(t, m, "", "fail on purpose")

	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errOut, "it broke") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMainPromptRespectsContextCancellation(t *testing.T) {
	home := setupHome(t)
	writeCommand(t, home, "long", `instruction """
Run something slow.
"""
run {
    print("started")
}
`)

	m := &fakeModel{reply: matchedReply("long", map[string]any{})}
	var out, errOut bytes.Buffer
	a := &app{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, newModel: fakeFactory(m)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := a.main(ctx, []string{"run something slow"}); code == ExitOK {
		t.Fatalf("a cancelled context should not report success")
	}
}

// --- helpers -------------------------------------------------------------

func TestValidateID(t *testing.T) {
	valid := []string{"thing", "git/status", "a/b/c", "with-dash", "with_underscore", "with.dot", "Mixed123"}
	for _, id := range valid {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{"", "/lead", "trail/", "a//b", "..", ".", "a/../b", "a/./b", "up/..", "sp ace", "we!rd", "-", "a\\b"}
	for _, id := range invalid {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) = nil, want an error", id)
		}
	}
}

func TestSummary(t *testing.T) {
	cases := map[string]string{
		"one line":          "one line",
		"first\nsecond":     "first",
		"  padded  ":        "padded",
		"first\n\nsecond":   "first",
		"\nleading newline": "leading newline",
		"":                  "",
		"\n\n\n":            "",
	}
	for in, want := range cases {
		if got := summary(in); got != want {
			t.Errorf("summary(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSystemFact(t *testing.T) {
	if got := systemFact("locale: en-US"); got != "locale: en-US" {
		t.Errorf("a configured system string should be used verbatim, got %q", got)
	}
	if got := systemFact("   "); !strings.HasPrefix(got, "date: ") {
		t.Errorf("blank should yield a date fact, got %q", got)
	}
	if got := systemFact(""); !strings.HasPrefix(got, "date: ") {
		t.Errorf("empty should yield a date fact, got %q", got)
	}
}

func TestResolveWeights(t *testing.T) {
	// A home with no model anywhere nearby.
	bare := t.TempDir()
	t.Setenv("HOME", bare)
	t.Setenv("USERPROFILE", bare)
	if _, err := resolveWeights(config.Config{}); err == nil {
		t.Error("expected an error when no model is anywhere")
	}

	// A configured path wins outright, whether or not it exists: the binding
	// reports a missing file with a better message than a guess would.
	got, err := resolveWeights(config.Config{Weights: "/explicit/needle3.cact"})
	if err != nil || got != "/explicit/needle3.cact" {
		t.Fatalf("resolveWeights = %q, %v", got, err)
	}

	// The conventional per-user location is found.
	home := setupHome(t)
	want := filepath.Join(home, ".needless", "models", "needle3.cact")
	if got, err := resolveWeights(config.Config{}); err != nil || got != want {
		t.Fatalf("resolveWeights = %q, %v; want %q", got, err, want)
	}
}
