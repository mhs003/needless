package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/nscript"
)

// run executes the run block of src with no arguments.
func run(t *testing.T, src string) (string, error) {
	t.Helper()
	return runArgs(t, src, nil)
}

func runArgs(t *testing.T, src string, args map[string]any) (string, error) {
	t.Helper()
	prog, err := nscript.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\nsource:\n%s", err, src)
	}
	var out bytes.Buffer
	e := &Executor{Out: &out, Err: &out}
	err = e.Run(context.Background(), prog, args)
	return out.String(), err
}

// mustRun asserts the script succeeds and returns its printed output.
func mustRun(t *testing.T, src string) string {
	t.Helper()
	out, err := run(t, src)
	if err != nil {
		t.Fatalf("run failed: %v\nsource:\n%s", err, src)
	}
	return out
}

func prog(t *testing.T, src string) *nscript.Program {
	t.Helper()
	p, err := nscript.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

// shell returns an interpreter available on this machine, or skips.
func shell(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"sh", "bash"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	t.Skip("no sh or bash available")
	return ""
}

// --- print and expressions ----------------------------------------------

func TestRunPrint(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    print("hello")
    print("world")
}
`)
	if got != "hello\nworld\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunExpressions(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    print(string(1 + 2 * 3))
    print(string((1 + 2) * 3))
    print(string(10 / 3))
    print(string(10.0 / 4.0))
    print("a" + "b")
    print(string(!false))
    print(string(-5))
    print(string(2 < 3))
    print(string(1 == 1.0))
}
`)
	want := "7\n9\n3\n2.5\nab\ntrue\n-5\ntrue\ntrue\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunArgumentAccess(t *testing.T) {
	got := mustRunWith(t, `instruction "x"
args {
    project: string
    port: int
    verbose: bool
}
run {
    print(project)
    print(string(port))
    print(string(verbose))
}
`, map[string]any{"project": "shop", "port": int64(3000), "verbose": true})
	if want := "shop\n3000\ntrue\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func mustRunWith(t *testing.T, src string, args map[string]any) string {
	t.Helper()
	out, err := runArgs(t, src, args)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	return out
}

func TestRunLet(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    let name = "shop"
    let count = 3
    let doubled = count * 2
    print(name + " " + string(doubled))
}
`)
	if want := "shop 6\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunIfElse(t *testing.T) {
	src := `instruction "x"
args {
    mode: string
}
run {
    if mode == "a" {
        print("got a")
    } else if mode == "b" {
        print("got b")
    } else {
        print("other")
    }
}
`
	for mode, want := range map[string]string{
		"a":   "got a\n",
		"b":   "got b\n",
		"zzz": "other\n",
	} {
		if got := mustRunWith(t, src, map[string]any{"mode": mode}); got != want {
			t.Errorf("mode %q gave %q, want %q", mode, got, want)
		}
	}
}

func TestRunShortCircuit(t *testing.T) {
	// The right operand must not be evaluated when the left decides the
	// result; `missing` is undefined, so evaluating it would fail.
	got := mustRun(t, `instruction "x"
run {
    if false && missing == 1 {
        print("no")
    }
    if true || missing == 1 {
        print("yes")
    }
}
`)
	if want := "yes\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunReturn(t *testing.T) {
	src := `instruction "x"
args {
    project: string
}
run {
    print("before")
    if project == "" {
        return
    }
    print("after")
}
`
	// An empty project returns early, so "after" is never printed.
	got := mustRunWith(t, src, map[string]any{"project": ""})
	if want := "before\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	got = mustRunWith(t, src, map[string]any{"project": "shop"})
	if want := "before\nafter\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunFunctions(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    fn greet(name: string) {
        print("Hello " + name)
    }
    fn double(n: int) {
        return
    }
    greet("world")
    greet("again")
    double(2)
}
`)
	if want := "Hello world\nHello again\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunFunctionArityWithDefaults(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    fn tag(name: string, prefix: string = "tag") {
        print(prefix + ":" + name)
    }
    tag("a")
    tag("b", "x")
}
`)
	if want := "tag:a\nx:b\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunFunctionScope(t *testing.T) {
	// The run block is one scope and functions see it, so a helper can use a
	// variable declared with `let`. A function's own locals stay inside it.
	got := mustRun(t, `instruction "x"
run {
    let greeting = "Hello"
    fn greet(name: string) {
        let punctuation = "!"
        print(greeting + " " + name + punctuation)
    }
    greet("world")
    print(greeting)
}
`)
	if want := "Hello world!\nHello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	// A local declared inside a function must not leak out to the run block.
	_, err := run(t, `instruction "x"
run {
    fn probe() {
        let hidden = "inner"
    }
    probe()
    print(hidden)
}
`)
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("err = %v, want a function local not to leak", err)
	}
}

func TestRunRecursionIsBounded(t *testing.T) {
	_, err := run(t, `instruction "x"
run {
    fn forever(n: int) {
        forever(n)
    }
    forever(1)
}
`)
	if err == nil {
		t.Fatal("expected recursion to be stopped")
	}
	if !strings.Contains(err.Error(), "call depth") {
		t.Fatalf("err = %v, want a call-depth error", err)
	}
}

func TestRunErrorBuiltin(t *testing.T) {
	out, err := run(t, `instruction "x"
run {
    print("before")
    error("Project was not found")
}
`)
	if err == nil {
		t.Fatal("expected an error")
	}
	// The message reaches the user; the runtime does not print it itself.
	if !strings.Contains(err.Error(), "Project was not found") {
		t.Fatalf("err = %v", err)
	}
	if out != "before\n" {
		t.Fatalf("output before the failure = %q", out)
	}
}

func TestRunStopsAtFirstFailure(t *testing.T) {
	out, err := run(t, `instruction "x"
run {
    error("stop")
    print("unreachable")
}
`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if out != "" {
		t.Fatalf("output = %q, want nothing printed", out)
	}
}

func TestRunErrorsCarryPosition(t *testing.T) {
	_, err := run(t, `instruction "x"
run {
    print("ok")
    print(nope)
}
`)
	if err == nil {
		t.Fatal("expected an error")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if rerr.Pos.Line != 4 {
		t.Fatalf("error at line %d, want 4 (%v)", rerr.Pos.Line, err)
	}
}

// --- type errors ---------------------------------------------------------

func TestRunTypeErrors(t *testing.T) {
	cases := map[string]string{
		`run { print("a" + 1) }`:                  "cannot add int to string",
		`run { print(string(1 + "a")) }`:          "needs two numbers",
		`run { print(string(!1)) }`:               "\"!\" needs a bool",
		`run { print(string(-"a")) }`:             "\"-\" needs a number",
		`run { if 1 { print("x") } }`:             "if needs a bool",
		`run { print(string(1 / 0)) }`:            "division by zero",
		`run { print(string(1.0 / 0.0)) }`:        "division by zero",
		`run { print(string(1 == "a")) }`:         "cannot compare",
		`run { print(string("a" < 1)) }`:          "same type",
		`run { print(string(true && 1)) }`:        "needs a bool",
		`run { print(string(int(1.5))) }`:         "fractional",
		`run { print(string(int("abc"))) }`:       "int() needs",
		`run { print(string(bool(1))) }`:          "bool() needs",
		`run { print(string(string(true) + 1)) }`: "cannot add int to string",
	}
	for body, wantSub := range cases {
		src := "instruction \"x\"\n" + body + "\n"
		_, err := run(t, src)
		if err == nil {
			t.Errorf("%s: expected an error", body)
			continue
		}
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("%s: error = %v, want it to contain %q", body, err, wantSub)
		}
	}
}

func TestRunStringWithAnyPrimitiveIsAllowed(t *testing.T) {
	got := mustRun(t, `instruction "x"
run {
    print(string("s"))
    print(string(1))
    print(string(1.5))
    print(string(true))
}
`)
	if want := "s\n1\n1.5\ntrue\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// --- exec ----------------------------------------------------------------

func TestRunExecRunsTheBody(t *testing.T) {
	sh := shell(t)
	got := mustRun(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<(\necho hello from the script\n)<<\n}\n")
	if !strings.Contains(got, "hello from the script") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunExecExposesEnvAndArgs(t *testing.T) {
	sh := shell(t)
	src := `instruction "x"
args {
    project: string
    port: int
    flag: bool
}
run {
    env {
        PROJECT = project
        PORT = port
        FLAG = flag
    }
    exec ` + sh + ` <<(
echo "P=$PROJECT N=$PORT F=$FLAG"
)<<
}
`
	got := mustRunWith(t, src, map[string]any{"project": "shop", "port": int64(3000), "flag": true})
	if want := "P=shop N=3000 F=true\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunExecBodyIsNotInterpolated(t *testing.T) {
	// A shell variable in the body must be expanded by the shell, not by
	// nscript, and an nscript name must not be substituted in.
	sh := shell(t)
	src := `instruction "x"
args {
    project: string
}
run {
    let local = "nscript"
    exec ` + sh + ` <<(
echo 'literal $project and $local'
)<<
}
`
	got := mustRunWith(t, src, map[string]any{"project": "shop"})
	if want := "literal $project and $local\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunExecEnvIsNotAnNscriptVariable(t *testing.T) {
	// An env binding feeds the child, not the nscript scope.
	sh := shell(t)
	_, err := run(t, `instruction "x"
run {
    env {
        PROJECT = "shop"
    }
    print(PROJECT)
    exec `+sh+` <<(true)<<
}
`)
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("err = %v, want an undefined-variable error", err)
	}
}

func TestRunExecEnvOverwrites(t *testing.T) {
	sh := shell(t)
	src := `instruction "x"
run {
    env {
        A = "first"
    }
    env {
        A = "second"
    }
    exec ` + sh + ` <<(
echo "A=$A"
)<<
}
`
	got := mustRun(t, src)
	if want := "A=second\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunExecFailureReportsExitStatus(t *testing.T) {
	sh := shell(t)
	out, err := run(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<(\necho failing\nexit 3\n)<<\n}\n")
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if rerr.Code != 3 {
		t.Fatalf("exit code = %d, want 3", rerr.Code)
	}
	if !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("err = %v", err)
	}
	// Output produced before the failure still reaches the user.
	if !strings.Contains(out, "failing") {
		t.Fatalf("output = %q, want the child's earlier output", out)
	}
}

func TestRunExecMissingInterpreter(t *testing.T) {
	_, err := run(t, "instruction \"x\"\nrun {\n    exec definitely-not-a-real-interpreter <<(\nx\n)<<\n}\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestRunExecStderrGoesToErr(t *testing.T) {
	sh := shell(t)
	prog := prog(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<(\necho to-stderr >&2\n)<<\n}\n")

	var out, errOut bytes.Buffer
	e := &Executor{Out: &out, Err: &errOut}
	if err := e.Run(context.Background(), prog, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want the child's stdout to be empty", out.String())
	}
	if !strings.Contains(errOut.String(), "to-stderr") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestRunExecNilOutDiscards(t *testing.T) {
	sh := shell(t)
	p := prog(t, "instruction \"x\"\nrun {\n    print(\"gone\")\n    exec "+sh+" <<(echo also-gone)<<\n}\n")
	if err := (&Executor{}).Run(context.Background(), p, nil); err != nil {
		t.Fatalf("a nil Out should discard, not fail: %v", err)
	}
}

func TestRunExecRespectsDir(t *testing.T) {
	sh := shell(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := prog(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<(\nls marker.txt\n)<<\n}\n")
	var out bytes.Buffer
	e := &Executor{Out: &out, Dir: dir}
	if err := e.Run(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "marker.txt") {
		t.Fatalf("output = %q, want the command to have run in %s", out.String(), dir)
	}
}

func TestRunExecRespectsContextCancellation(t *testing.T) {
	sh := shell(t)
	p := prog(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<(sleep 5)<<\n}\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Executor{Out: &bytes.Buffer{}}).Run(ctx, p, nil)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("err = %v, want it to mention cancellation", err)
	}
}

func TestRunExecEmptyBody(t *testing.T) {
	sh := shell(t)
	if got := mustRun(t, "instruction \"x\"\nrun {\n    exec "+sh+" <<()<<\n}\n"); got != "" {
		t.Fatalf("output = %q, want nothing", got)
	}
}

// --- Program-level behaviour --------------------------------------------

func TestRunRequiresArgumentValues(t *testing.T) {
	p := prog(t, `instruction "x"
args {
    project: string
}
run { print(project) }
`)
	// The matcher always supplies defaults; a missing value here is a caller
	// bug, and must be reported rather than silently becoming an empty string.
	err := (&Executor{Out: &bytes.Buffer{}}).Run(context.Background(), p, nil)
	if err == nil || !strings.Contains(err.Error(), `no value for argument "project"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNilProgram(t *testing.T) {
	if err := (&Executor{}).Run(context.Background(), nil, nil); err == nil {
		t.Fatal("expected an error for a nil program")
	}
	if err := (&Executor{}).Run(context.Background(), &nscript.Program{}, nil); err == nil {
		t.Fatal("expected an error for a program with no run block")
	}
}

func TestRequiresConfirmation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"declared true", "instruction \"x\"\nconfirm true\nrun { print(\"y\") }\n", true},
		{"declared false", "instruction \"x\"\nconfirm false\nrun { print(\"y\") }\n", false},
		{"not declared", "instruction \"x\"\nrun { print(\"y\") }\n", false}, // the default is opt-in (D12)
	}
	for _, tc := range cases {
		if got := RequiresConfirmation(prog(t, tc.src)); got != tc.want {
			t.Errorf("%s: RequiresConfirmation = %v, want %v", tc.name, got, tc.want)
		}
	}
	if RequiresConfirmation(nil) {
		t.Error("RequiresConfirmation(nil) = true, want false")
	}
}
