package intent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/nscript"
	"github.com/mhs003/needless/needle"
)

// fakeModel returns a canned reply, so matching is tested without the model.
type fakeModel struct {
	reply   needle.Result
	err     error
	gotIn   string
	gotMax  int
	replies []needle.Result // consumed in order when non-empty
}

func (f *fakeModel) CompleteResult(_ context.Context, input string, maxNewTokens int) (needle.Result, error) {
	f.gotIn, f.gotMax = input, maxNewTokens
	if f.err != nil {
		return needle.Result{}, f.err
	}
	if len(f.replies) > 0 {
		r := f.replies[0]
		f.replies = f.replies[1:]
		return r, nil
	}
	return f.reply, nil
}

// command builds a Command from source, the same way discovery would.
func command(t *testing.T, id, src string) commands.Command {
	t.Helper()
	prog, err := nscript.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse %s: %v", id, err)
	}
	return commands.Command{ID: id, Path: id + ".nsc", Program: prog}
}

func call(name string, args map[string]any) needle.Result {
	raw, _ := json.Marshal(args)
	return needle.Result{
		Type:          "call",
		Success:       true,
		FunctionCalls: []needle.FunctionCall{{Name: name, Arguments: raw}},
	}
}

func deployment(t *testing.T) commands.Command {
	t.Helper()
	return command(t, "project/start", `
instruction """
Start the development server for a project.
"""
args {
    project: string
    port: int = 8000
    detached: bool = true
    ratio: float = 0.5
}
run { print("go") }
`)
}

// --- schema --------------------------------------------------------------

func TestToolsJSONShape(t *testing.T) {
	cmd := deployment(t)

	got, err := ToolsJSON([]commands.Command{cmd})
	if err != nil {
		t.Fatal(err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("ToolsJSON produced invalid JSON: %v\n%s", err, got)
	}
	if len(decoded) != 1 {
		t.Fatalf("tools = %d, want 1", len(decoded))
	}

	tool := decoded[0]
	if tool["name"] != "project/start" {
		t.Errorf("name = %v, want the command ID verbatim", tool["name"])
	}
	if !strings.Contains(tool["description"].(string), "Start the development server") {
		t.Errorf("description = %v", tool["description"])
	}

	params := tool["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v", params["type"])
	}

	props := params["properties"].(map[string]any)
	wantTypes := map[string]string{
		"project":  "string",
		"port":     "integer",
		"detached": "boolean",
		"ratio":    "number",
	}
	if len(props) != len(wantTypes) {
		t.Fatalf("properties = %v, want %d entries", props, len(wantTypes))
	}
	for name, want := range wantTypes {
		p, ok := props[name].(map[string]any)
		if !ok {
			t.Fatalf("property %q = %v", name, props[name])
		}
		if p["type"] != want {
			t.Errorf("property %q type = %v, want %v", name, p["type"], want)
		}
	}

	// Only the argument without a default is required.
	req := params["required"].([]any)
	if len(req) != 1 || req[0] != "project" {
		t.Errorf("required = %v, want [project]", req)
	}
}

func TestToolsJSONStatesDefaultsIncludingFalsey(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    off: bool = false
    zero: int = 0
    empty: string = ""
    rate: float = 0.0
}
run { print("y") }
`)
	got, err := ToolsJSON([]commands.Command{cmd})
	if err != nil {
		t.Fatal(err)
	}

	var decoded []struct {
		Parameters struct {
			Properties map[string]struct {
				Type    string `json:"type"`
				Default *any   `json:"default"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	props := decoded[0].Parameters.Properties

	// A `false`, `0`, or `""` default must still appear: those are exactly the
	// values a naive omitempty would drop.
	for name, want := range map[string]any{
		"off":   false,
		"zero":  float64(0),
		"empty": "",
		"rate":  float64(0),
	} {
		p, ok := props[name]
		if !ok {
			t.Fatalf("property %q missing", name)
		}
		if p.Default == nil {
			t.Fatalf("property %q has no default in the schema", name)
		}
		if *p.Default != want {
			t.Errorf("property %q default = %#v, want %#v", name, *p.Default, want)
		}
	}
	if len(decoded[0].Parameters.Required) != 0 {
		t.Errorf("required = %v, want empty", decoded[0].Parameters.Required)
	}
}

func TestToolsJSONIsSortedAndStable(t *testing.T) {
	cmds := []commands.Command{
		command(t, "zebra", `instruction "z"`+"\nrun { print(\"y\") }"),
		command(t, "alpha", `instruction "a"`+"\nrun { print(\"y\") }"),
		command(t, "mango", `instruction "m"`+"\nrun { print(\"y\") }"),
	}

	first, err := ToolsJSON(cmds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ToolsJSON(cmds)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("ToolsJSON is not deterministic")
	}

	var decoded []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(first), &decoded); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range decoded {
		names = append(names, d.Name)
	}
	if want := []string{"alpha", "mango", "zebra"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestToolsJSONNoCommands(t *testing.T) {
	got, err := ToolsJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "[]" {
		t.Fatalf("ToolsJSON(nil) = %q, want %q", got, "[]")
	}
}

// --- matching ------------------------------------------------------------

func TestMatchSelectsCommandAndAppliesDefaults(t *testing.T) {
	cmd := deployment(t)
	model := &fakeModel{reply: call("project/start", map[string]any{"project": "shop"})}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "start the shop project")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Fatal("expected a match")
	}
	if res.Command.ID != "project/start" {
		t.Errorf("command = %q", res.Command.ID)
	}
	want := map[string]any{
		"project":  "shop",
		"port":     int64(8000),
		"detached": true,
		"ratio":    0.5,
	}
	if !reflect.DeepEqual(res.Args, want) {
		t.Fatalf("args = %#v, want %#v", res.Args, want)
	}
	if model.gotIn != "start the shop project" {
		t.Errorf("prompt passed = %q", model.gotIn)
	}
	if model.gotMax != DefaultMaxTokens {
		t.Errorf("maxNewTokens = %d, want %d", model.gotMax, DefaultMaxTokens)
	}
}

func TestMatchRefusalIsNotAnError(t *testing.T) {
	cmd := deployment(t)
	model := &fakeModel{reply: needle.Result{
		Type:       "respond",
		Success:    true,
		Reasoning:  "nothing to do",
		Confidence: ptr(0.2),
	}}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "what is the weather in Paris?")
	if err != nil {
		t.Fatalf("a refusal must not be an error: %v", err)
	}
	if res.Matched {
		t.Fatal("a refusal must not report a match")
	}
	if len(res.Args) != 0 {
		t.Errorf("args = %v, want none", res.Args)
	}
	if res.Reasoning != "nothing to do" {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
}

func TestMatchReportsSuppressedCalls(t *testing.T) {
	cmd := deployment(t)
	args, _ := json.Marshal(map[string]any{"project": "admin"})
	model := &fakeModel{reply: needle.Result{
		Type:            "call",
		FunctionCalls:   nil,
		SuppressedCalls: []needle.FunctionCall{{Name: "project/start", Arguments: args}},
		Confidence:      ptr(0.05),
	}}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "restart the admin project")
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched {
		t.Fatal("a withheld call must not be executed")
	}
	if len(res.Suppressed) != 1 || res.Suppressed[0].Name != "project/start" {
		t.Fatalf("suppressed = %+v, want the withheld call reported", res.Suppressed)
	}
}

func TestMatchNoCommands(t *testing.T) {
	m := New(&fakeModel{}, nil)
	_, err := m.Match(context.Background(), "do something")
	if !errors.Is(err, ErrNoCommands) {
		t.Fatalf("err = %v, want ErrNoCommands", err)
	}
}

func TestMatchPropagatesModelError(t *testing.T) {
	model := &fakeModel{err: errors.New("engine exploded")}
	m := New(model, []commands.Command{deployment(t)})

	_, err := m.Match(context.Background(), "start it")
	if err == nil || !strings.Contains(err.Error(), "engine exploded") {
		t.Fatalf("err = %v, want it to wrap the model error", err)
	}
}

func TestMatchRejectsUndeclaredCommand(t *testing.T) {
	model := &fakeModel{reply: call("rm_rf", map[string]any{})}
	m := New(model, []commands.Command{deployment(t)})

	_, err := m.Match(context.Background(), "delete everything")
	if err == nil {
		t.Fatal("expected an error for a name outside the declared set")
	}
	if !strings.Contains(err.Error(), "not a declared command") {
		t.Fatalf("err = %v", err)
	}
}

func TestMatchAnOmittedRequiredArgumentIsARefusal(t *testing.T) {
	// The schema marks project as required, so the grammar should not let the
	// model omit it. If it happens anyway there is still nothing runnable, and
	// reporting that as a refusal keeps it the same kind of outcome as an
	// empty value (D50).
	model := &fakeModel{reply: call("project/start", map[string]any{})}
	m := New(model, []commands.Command{deployment(t)})

	res, err := m.Match(context.Background(), "start something")
	if err != nil {
		t.Fatalf("an unfillable command is a refusal, not an error: %v", err)
	}
	if res.Matched {
		t.Fatal("a command with no value for a required argument must not match")
	}
	if !reflect.DeepEqual(res.Unfilled, []string{"project"}) {
		t.Fatalf("Unfilled = %v, want [project]", res.Unfilled)
	}
	if res.Command.ID != "project/start" {
		t.Errorf("Command = %q, want the recognised command named", res.Command.ID)
	}
	if len(res.Args) != 0 {
		t.Errorf("Args = %v, want none", res.Args)
	}
}

// TestMatchEmptyStringDoesNotSatisfyARequiredArgument is the regression test
// for B1. The model has nothing to put in `project`, so it returns "", which
// used to be taken at face value: the command ran, and failed inside its own
// shell script on an empty $PROJECT — "not a git repository: " with nothing
// after the colon. It must instead be reported as an unfilled argument.
func TestMatchEmptyStringDoesNotSatisfyARequiredArgument(t *testing.T) {
	model := &fakeModel{reply: call("project/start", map[string]any{"project": ""})}
	m := New(model, []commands.Command{deployment(t)})

	res, err := m.Match(context.Background(), "show me git status")
	if err != nil {
		t.Fatalf("an empty value is a refusal, not an error: %v", err)
	}
	if res.Matched {
		t.Fatalf("an empty string satisfied a required argument: args = %#v", res.Args)
	}
	if !reflect.DeepEqual(res.Unfilled, []string{"project"}) {
		t.Fatalf("Unfilled = %v, want [project]", res.Unfilled)
	}
	// Nothing may reach the runtime with a hole where a required value goes.
	if _, ok := res.Args["project"]; ok {
		t.Errorf("Args carries project = %#v, want no value at all", res.Args["project"])
	}
}

// TestMatchEmptyStringFallsBackToTheDefault pins the second half of D51: an
// empty value is read as "not supplied", so an argument that has a default
// takes it rather than being overridden with an empty string. The rule is
// uniform across types, which is why the int and bool are here too: "" cannot
// be coerced to either, so reading it as an error would make the rule an
// exception for everything that is not a string.
func TestMatchEmptyStringFallsBackToTheDefault(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    branch: string = "main"
    note: string = ""
    port: int = 8000
    flag: bool = true
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{
		"branch": "", "note": "", "port": "", "flag": "",
	})}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Fatalf("every argument has a default, so this is runnable: %+v", res)
	}
	if got := res.Args["branch"]; got != "main" {
		t.Errorf("branch = %#v, want the default %q rather than an empty override", got, "main")
	}
	if got := res.Args["port"]; got != int64(8000) {
		t.Errorf("port = %#v, want the default 8000", got)
	}
	if got := res.Args["flag"]; got != true {
		t.Errorf("flag = %#v, want the default true", got)
	}
	// An explicit `= ""` is the author opting into an empty value, and it
	// still arrives as one.
	if got := res.Args["note"]; got != "" {
		t.Errorf("note = %#v, want %q", got, "")
	}
	if len(res.Unfilled) != 0 {
		t.Errorf("Unfilled = %v, want none", res.Unfilled)
	}
}

// TestMatchEmptyStringLeavesARequiredNonStringUnfilled covers the other side of
// the same rule: with no default to fall back on, "" is an unfilled argument
// rather than a coercion error.
func TestMatchEmptyStringLeavesARequiredNonStringUnfilled(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    port: int
    flag: bool
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"port": "", "flag": ""})}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "go")
	if err != nil {
		t.Fatalf("an empty value is a refusal, not a coercion error: %v", err)
	}
	if res.Matched {
		t.Fatalf("expected no match, got args %#v", res.Args)
	}
	if want := []string{"port", "flag"}; !reflect.DeepEqual(res.Unfilled, want) {
		t.Fatalf("Unfilled = %v, want %v", res.Unfilled, want)
	}
}

// TestMatchFillerValuesMeanNoValue is the regression test for the second face
// of B1. A model with nothing to put in a slot does not always send ""; it also
// sends JSON null, or a bare boolean. `false` for a `string` slot used to be
// coerced to the string "false" — the worst possible outcome, because it looks
// like a value — and reached a shell script as `cd false`:
//
//	$ n show me the git status
//	not a git repository: false
//
// All three shapes mean "nothing" and must leave the argument unfilled (D51).
func TestMatchFillerValuesMeanNoValue(t *testing.T) {
	// Each of these declares a required string with no default.
	cmd := command(t, "git/status", `instruction "Show the git status of a project."
args {
    project: string
    note: string = "defaulted"
    flag: bool = true
}
run { print("y") }
`)

	cases := []struct {
		name  string
		value any
	}{
		{name: "empty string", value: ""},
		{name: "null", value: nil},
		{name: "false", value: false},
		{name: "true", value: true},
		// Observed from the real model: one run in ten sent the *string*
		// "false" rather than a boolean, which no type-based rule can catch.
		{name: "the quoted string \"false\"", value: "false"},
		{name: "the quoted string \"TRUE\"", value: "TRUE"},
		{name: "the quoted string \"null\"", value: "null"},
		{name: "the quoted string \"none\"", value: "none"},
		{name: "the quoted string \"nil\"", value: "nil"},
		{name: "the quoted string \"undefined\"", value: "undefined"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{reply: call("git/status", map[string]any{
				"project": tc.value,
				"note":    tc.value,
				"flag":    tc.value,
			})}
			res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "show me the git status")
			if err != nil {
				t.Fatalf("a meaningless value is a refusal, not an error: %v", err)
			}

			// The required argument is unfilled, so nothing runs at all.
			if res.Matched {
				t.Fatalf("matched with project = %#v, which is a value invented from nothing", res.Args["project"])
			}
			if !reflect.DeepEqual(res.Unfilled, []string{"project"}) {
				t.Fatalf("Unfilled = %v, want [project]", res.Unfilled)
			}
			if _, ok := res.Args["project"]; ok {
				t.Errorf("Args carries project = %#v", res.Args["project"])
			}
		})
	}
}

// TestMatchFillerValuesFallBackToDefaults is the other half: an argument that
// has a default takes it rather than being overwritten with filler.
func TestMatchFillerValuesFallBackToDefaults(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    branch: string = "main"
    port: int = 8000
    flag: bool = false
}
run { print("y") }
`)

	// Only the string and int slots are given filler. A boolean in the bool
	// slot would be a value, not filler, which TestMatchBooleanSlotsStillTake
	// Booleans covers.
	for _, filler := range []any{"", nil, false, true} {
		model := &fakeModel{reply: call("x", map[string]any{
			"branch": filler,
			"port":   filler,
		})}
		res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
		if err != nil {
			t.Fatalf("filler %#v: %v", filler, err)
		}
		if !res.Matched {
			t.Fatalf("filler %#v: every argument has a default, so this is runnable", filler)
		}
		if got := res.Args["branch"]; got != "main" {
			t.Errorf("filler %#v: branch = %#v, want the default", filler, got)
		}
		if got := res.Args["port"]; got != int64(8000) {
			t.Errorf("filler %#v: port = %#v, want the default", filler, got)
		}
		// Omitted entirely, so it takes its default.
		if got := res.Args["flag"]; got != false {
			t.Errorf("filler %#v: flag = %#v, want the default", filler, got)
		}
	}
}

// TestMatchBooleanSlotsStillTakeBooleans guards the boundary of the rule: a
// boolean in a boolean slot is a value, not filler, so `false` must survive.
func TestMatchBooleanSlotsStillTakeBooleans(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    flag: bool
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"flag": false})}
	res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Fatalf("false is a legitimate value for a bool slot: %+v", res)
	}
	if got := res.Args["flag"]; got != false {
		t.Errorf("flag = %#v, want false", got)
	}
}

// TestMatchNumbersAreStillCoercedToStrings guards the other boundary: a number
// for a string slot has a real reading, so it is converted rather than treated
// as filler (D29).
func TestMatchNumbersAreStillCoercedToStrings(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    project: string
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"project": 3000})}
	res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched || res.Args["project"] != "3000" {
		t.Fatalf("matched = %v, project = %#v; want the number rendered as a string", res.Matched, res.Args["project"])
	}
}

// TestMatchFillerWordsAreNotValues pins the escape hatch for the filler-word
// rule: a command that genuinely wants one of those strings declares it as a
// default and receives it, because filler falls back to the default.
func TestMatchFillerWordsAreNotValues(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    literal: string = "false"
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"literal": "false"})}
	res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Fatalf("an argument with a default is always runnable: %+v", res)
	}
	if got := res.Args["literal"]; got != "false" {
		t.Errorf("literal = %#v, want %q", got, "false")
	}
}

// TestMatchAQuotedBooleanStillWorksForABoolSlot is the other boundary: "false"
// in a bool slot is a representation of false, not filler.
func TestMatchAQuotedBooleanStillWorksForABoolSlot(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    flag: bool
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"flag": "false"})}
	res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched || res.Args["flag"] != false {
		t.Fatalf("matched = %v, flag = %#v; want the quoted boolean read as false", res.Matched, res.Args["flag"])
	}
}

// TestMatchABareWordIsStillAValue guards the edge of the word list: anything not
// on it is an ordinary string and must pass through untouched.
func TestMatchABareWordIsStillAValue(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    project: string
}
run { print("y") }
`)
	for _, word := range []string{"nothing", "nonexistent", "n/a", "0", "yes"} {
		model := &fakeModel{reply: call("x", map[string]any{"project": word})}
		res, err := New(model, []commands.Command{cmd}).Match(context.Background(), "go")
		if err != nil {
			t.Fatalf("%q: %v", word, err)
		}
		if !res.Matched {
			t.Errorf("%q was treated as filler, but it is an ordinary string", word)
			continue
		}
		if got := res.Args["project"]; got != word {
			t.Errorf("project = %#v, want %q untouched", got, word)
		}
	}
}

// TestMatchWhitespaceIsAValue guards the boundary of the rule: only the empty
// string means "absent". A deliberate space is a value the author asked for,
// and trimming it would be Needless editing the user's words.
func TestMatchWhitespaceIsAValue(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    marker: string
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"marker": " "})}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Fatalf("a space is a value: %+v", res)
	}
	if got := res.Args["marker"]; got != " " {
		t.Errorf("marker = %#v, want a single space", got)
	}
}

// TestMatchUnfilledArgumentsAreInDeclarationOrder keeps the reported names
// stable, so the message the user sees does not shuffle between runs.
func TestMatchUnfilledArgumentsAreInDeclarationOrder(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    zebra: string
    alpha: string
    middle: string = "given"
}
run { print("y") }
`)
	model := &fakeModel{reply: call("x", map[string]any{"zebra": "", "alpha": ""})}
	m := New(model, []commands.Command{cmd})

	res, err := m.Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"zebra", "alpha"}; !reflect.DeepEqual(res.Unfilled, want) {
		t.Fatalf("Unfilled = %v, want %v", res.Unfilled, want)
	}
}

func TestMatchRejectsUndeclaredArgument(t *testing.T) {
	// A value the script never declared cannot be used, so accepting it would
	// silently discard part of what the model understood.
	model := &fakeModel{reply: call("project/start", map[string]any{
		"project": "shop",
		"sudo":    true,
	})}
	m := New(model, []commands.Command{deployment(t)})

	_, err := m.Match(context.Background(), "start the shop project as root")
	if err == nil || !strings.Contains(err.Error(), `undeclared argument(s) sudo`) {
		t.Fatalf("err = %v", err)
	}
}

func TestMatchCoercesTypes(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    count: int
    rate: float
    flag: bool
    note: string
}
run { print("y") }
`)
	cases := []struct {
		name  string
		args  map[string]any
		want  map[string]any
		isErr bool
	}{
		{
			name: "native JSON types",
			args: map[string]any{"count": 3, "rate": 1.5, "flag": true, "note": "hi"},
			want: map[string]any{"count": int64(3), "rate": 1.5, "flag": true, "note": "hi"},
		},
		{
			name: "quoted numbers are accepted",
			args: map[string]any{"count": "4", "rate": "2.5", "flag": "true", "note": "hi"},
			want: map[string]any{"count": int64(4), "rate": 2.5, "flag": true, "note": "hi"},
		},
		{
			name: "numbers written for strings are rendered",
			args: map[string]any{"count": 1, "rate": 1.0, "flag": "false", "note": 3000},
			want: map[string]any{"count": int64(1), "rate": 1.0, "flag": false, "note": "3000"},
		},
		{
			name:  "a fractional value is not an integer",
			args:  map[string]any{"count": 1.5, "rate": 1.0, "flag": true, "note": "x"},
			isErr: true,
		},
		{
			name:  "a non-numeric string is not a number",
			args:  map[string]any{"count": "many", "rate": 1.0, "flag": true, "note": "x"},
			isErr: true,
		},
		{
			name:  "a number is not a boolean",
			args:  map[string]any{"count": 1, "rate": 1.0, "flag": 1, "note": "x"},
			isErr: true,
		},
		{
			name:  "a nested object is rejected",
			args:  map[string]any{"count": 1, "rate": 1.0, "flag": true, "note": map[string]any{}},
			isErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&fakeModel{reply: call("x", tc.args)}, []commands.Command{cmd})
			res, err := m.Match(context.Background(), "go")
			if tc.isErr {
				if err == nil {
					t.Fatalf("expected an error, got args %#v", res.Args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(res.Args, tc.want) {
				t.Fatalf("args = %#v, want %#v", res.Args, tc.want)
			}
		})
	}
}

func TestMatchPassesThroughConfidence(t *testing.T) {
	model := &fakeModel{reply: needle.Result{
		Type:          "call",
		FunctionCalls: []needle.FunctionCall{{Name: "project/start", Arguments: json.RawMessage(`{"project":"a"}`)}},
		Confidence:    ptr(0.87),
		Reasoning:     "a -> project",
	}}
	m := New(model, []commands.Command{deployment(t)})

	res, err := m.Match(context.Background(), "start a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Confidence == nil || *res.Confidence != 0.87 {
		t.Fatalf("confidence = %v, want 0.87", res.Confidence)
	}
	if res.Reasoning != "a -> project" {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
}

func TestMatchUsesOnlyTheFirstCall(t *testing.T) {
	// The model may in principle return several calls; Needless executes one
	// command, so the first is taken and the rest ignored.
	a, _ := json.Marshal(map[string]any{"project": "first"})
	b, _ := json.Marshal(map[string]any{"project": "second"})
	model := &fakeModel{reply: needle.Result{
		Type: "call",
		FunctionCalls: []needle.FunctionCall{
			{Name: "project/start", Arguments: a},
			{Name: "project/start", Arguments: b},
		},
	}}
	m := New(model, []commands.Command{deployment(t)})

	res, err := m.Match(context.Background(), "start both")
	if err != nil {
		t.Fatal(err)
	}
	if res.Args["project"] != "first" {
		t.Fatalf("project = %v, want the first call to win", res.Args["project"])
	}
}

func TestSetMaxNewTokens(t *testing.T) {
	model := &fakeModel{reply: call("project/start", map[string]any{"project": "a"})}
	m := New(model, []commands.Command{deployment(t)})

	m.SetMaxNewTokens(64)
	if _, err := m.Match(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if model.gotMax != 64 {
		t.Fatalf("maxNewTokens = %d, want 64", model.gotMax)
	}

	// Zero restores the default rather than sending a nonsense budget.
	m.SetMaxNewTokens(0)
	if _, err := m.Match(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if model.gotMax != DefaultMaxTokens {
		t.Fatalf("maxNewTokens = %d, want the default", model.gotMax)
	}
}

func TestDefaultArgs(t *testing.T) {
	// A command whose args all have defaults resolves cleanly.
	allDefault := command(t, "fb", `instruction "x"
args {
    note: string = "fallback"
    count: int = 3
    flag: bool = false
}
run { print("y") }
`)
	got, err := DefaultArgs(allDefault)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"note": "fallback", "count": int64(3), "flag": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultArgs = %#v, want %#v", got, want)
	}

	// A command with no args at all resolves to an empty map, not nil, so the
	// runtime can index it without a nil check.
	noArgs := command(t, "none", `instruction "x"`+"\nrun { print(\"y\") }")
	got, err = DefaultArgs(noArgs)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("DefaultArgs = %#v, want an empty map", got)
	}
}

func TestDefaultArgsRejectsARequiredArgument(t *testing.T) {
	// deployment's project argument has no default.
	_, err := DefaultArgs(deployment(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "cannot be used as a fallback") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "project") {
		t.Fatalf("err = %v, want it to name the argument", err)
	}
}

func TestFormatNumber(t *testing.T) {
	cases := map[float64]string{
		0:      "0",
		3000:   "3000",
		-42:    "-42",
		1.5:    "1.5",
		0.25:   "0.25",
		1e15:   "1000000000000000",
		1.0e16: "10000000000000000",
	}
	for in, want := range cases {
		if got := formatNumber(in); got != want {
			t.Errorf("formatNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"x", strconv.Quote("x")},
		{nil, "null"},
		{map[string]any{}, "a composite value"},
		{[]any{}, "a composite value"},
		{true, "true"},
		{1.5, "1.5"},
	}
	for _, tc := range cases {
		if got := describe(tc.in); got != tc.want {
			t.Errorf("describe(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }
