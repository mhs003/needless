package nscript

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Program {
	t.Helper()
	prog, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse failed: %v\nsource:\n%s", err, src)
	}
	return prog
}

// parseErr asserts that src fails to parse, and returns the message.
func parseErr(t *testing.T, src string) string {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatalf("expected a parse error for:\n%s", src)
	}
	return err.Error()
}

func wantErrContains(t *testing.T, src, sub string) {
	t.Helper()
	got := parseErr(t, src)
	if !strings.Contains(got, sub) {
		t.Fatalf("error = %q\nwant it to contain %q\nsource:\n%s", got, sub, src)
	}
}

// --- whole programs ------------------------------------------------------

func TestParseMinimalCommand(t *testing.T) {
	prog := mustParse(t, `
instruction """
Start the development server for a project.
"""

args {
    project: string
    port: int = 8000
}

run {
    exec bash <<(
cd "$project"
php artisan serve --port "$port"
)<<
}
`)

	if got, want := prog.Instruction, "Start the development server for a project."; got != want {
		t.Errorf("instruction = %q, want %q", got, want)
	}
	if len(prog.Args) != 2 {
		t.Fatalf("args = %d, want 2", len(prog.Args))
	}
	if prog.Args[0].Name != "project" || prog.Args[0].Type != TypeString {
		t.Errorf("arg 0 = %+v", prog.Args[0])
	}
	if prog.Args[0].Default != nil {
		t.Error("arg 0 should have no default")
	}
	if prog.Args[1].Name != "port" || prog.Args[1].Type != TypeInt {
		t.Errorf("arg 1 = %+v", prog.Args[1])
	}
	def, ok := prog.Args[1].Default.(*IntLit)
	if !ok || def.Value != 8000 {
		t.Errorf("arg 1 default = %#v, want IntLit(8000)", prog.Args[1].Default)
	}
	if prog.Confirm != nil {
		t.Error("confirm should be nil when not declared")
	}
	if prog.Run == nil || len(prog.Run.Stmts) != 1 {
		t.Fatalf("run block = %+v", prog.Run)
	}
	exec, ok := prog.Run.Stmts[0].(*ExecStmt)
	if !ok {
		t.Fatalf("run stmt = %T, want *ExecStmt", prog.Run.Stmts[0])
	}
	if exec.Interpreter != "bash" {
		t.Errorf("interpreter = %q", exec.Interpreter)
	}
	wantBody := "\ncd \"$project\"\nphp artisan serve --port \"$port\"\n"
	if string(exec.Body) != wantBody {
		t.Errorf("body = %q, want %q", exec.Body, wantBody)
	}
}

func TestParseSpecExampleEnvBlock(t *testing.T) {
	prog := mustParse(t, `
instruction """
Start the development server for a project.
"""

args {
    project: string
    port: int = 8000
}

run {
    env {
        PROJECT = project
        PORT = port
    }

    exec bash <<(
set -e

cd "$PROJECT"
php artisan serve --port "$PORT"

)<<
}
`)
	if len(prog.Run.Stmts) != 2 {
		t.Fatalf("run has %d statements, want 2", len(prog.Run.Stmts))
	}
	env, ok := prog.Run.Stmts[0].(*EnvStmt)
	if !ok {
		t.Fatalf("first statement = %T, want *EnvStmt", prog.Run.Stmts[0])
	}
	if len(env.Vars) != 2 {
		t.Fatalf("env vars = %d, want 2", len(env.Vars))
	}
	if env.Vars[0].Name != "PROJECT" || env.Vars[1].Name != "PORT" {
		t.Errorf("env names = %q, %q", env.Vars[0].Name, env.Vars[1].Name)
	}
	if _, ok := env.Vars[0].Value.(*IdentExpr); !ok {
		t.Errorf("PROJECT value = %T, want *IdentExpr", env.Vars[0].Value)
	}
	if _, ok := prog.Run.Stmts[1].(*ExecStmt); !ok {
		t.Errorf("second statement = %T, want *ExecStmt", prog.Run.Stmts[1])
	}
}

func TestParseSpecExampleNativeConstructs(t *testing.T) {
	prog := mustParse(t, `
instruction """
Demonstrate the native constructs.
"""

args {
    project: string
}

run {
    let project_path = "/home/user/project"
    if project == "backend" {
        print("Backend selected")
    } else {
        print("Other project selected")
    }
    fn greet(name: string) {
        print("Hello " + name)
    }
    if project == "" {
        return
    }
    print("Project: " + project)
    error("Project was not found")
}
`)
	stmts := prog.Run.Stmts
	if len(stmts) != 6 {
		t.Fatalf("statements = %d, want 6", len(stmts))
	}
	if _, ok := stmts[0].(*LetStmt); !ok {
		t.Errorf("stmt 0 = %T, want *LetStmt", stmts[0])
	}
	ifs, ok := stmts[1].(*IfStmt)
	if !ok {
		t.Fatalf("stmt 1 = %T, want *IfStmt", stmts[1])
	}
	if ifs.Else == nil {
		t.Error("expected an else block")
	}
	if _, ok := stmts[2].(*FnStmt); !ok {
		t.Errorf("stmt 2 = %T, want *FnStmt", stmts[2])
	}
	// The second `if` has no else.
	if second, ok := stmts[3].(*IfStmt); !ok || second.Else != nil {
		t.Errorf("stmt 3 = %T (%+v), want an *IfStmt with no else", stmts[3], second)
	}
	if _, ok := stmts[4].(*ExprStmt); !ok {
		t.Errorf("stmt 4 = %T, want *ExprStmt", stmts[4])
	}
}

func TestParseConfirmPolicies(t *testing.T) {
	prog := mustParse(t, "instruction \"x\"\nconfirm true\nrun { print(\"y\") }")
	if prog.Confirm == nil || *prog.Confirm != true {
		t.Fatalf("confirm = %v, want true", prog.Confirm)
	}

	prog = mustParse(t, "instruction \"x\"\nconfirm false\nrun { print(\"y\") }")
	if prog.Confirm == nil || *prog.Confirm != false {
		t.Fatalf("confirm = %v, want false", prog.Confirm)
	}
}

func TestParseInstructionForms(t *testing.T) {
	// Triple-quoted is trimmed of the whitespace created by the delimiters.
	prog := mustParse(t, "instruction \"\"\"\n  Do a thing.\n\n  And another.\n\"\"\"\nrun { print(\"x\") }")
	if want := "Do a thing.\n\n  And another."; prog.Instruction != want {
		t.Errorf("instruction = %q, want %q", prog.Instruction, want)
	}

	// A plain string is taken exactly as written.
	prog = mustParse(t, "instruction \"  spaced  \"\nrun { print(\"x\") }")
	if prog.Instruction != "  spaced  " {
		t.Errorf("instruction = %q, want it preserved", prog.Instruction)
	}
}

func TestParseArgTypesAndDefaults(t *testing.T) {
	prog := mustParse(t, `
instruction "x"
args {
    a: string = "s"
    b: int = 1
    c: float = 2.5
    d: bool = true
    e: float = 3
    f: string
}
run { print("x") }
`)
	want := []struct {
		name string
		typ  Type
		def  any
	}{
		{"a", TypeString, "s"},
		{"b", TypeInt, int64(1)},
		{"c", TypeFloat, 2.5},
		{"d", TypeBool, true},
		{"e", TypeFloat, int64(3)}, // an integer literal is a valid float default
		{"f", TypeString, nil},
	}
	if len(prog.Args) != len(want) {
		t.Fatalf("args = %d, want %d", len(prog.Args), len(want))
	}
	for i, w := range want {
		got := prog.Args[i]
		if got.Name != w.name || got.Type != w.typ {
			t.Errorf("arg %d = %s %s, want %s %s", i, got.Name, got.Type, w.name, w.typ)
		}
		switch d := w.def.(type) {
		case nil:
			if got.Default != nil {
				t.Errorf("arg %s default = %#v, want none", w.name, got.Default)
			}
		case string:
			if lit, ok := got.Default.(*StringLit); !ok || lit.Value != d {
				t.Errorf("arg %s default = %#v, want %q", w.name, got.Default, d)
			}
		case int64:
			if lit, ok := got.Default.(*IntLit); !ok || lit.Value != d {
				t.Errorf("arg %s default = %#v, want %d", w.name, got.Default, d)
			}
		case float64:
			if lit, ok := got.Default.(*FloatLit); !ok || lit.Value != d {
				t.Errorf("arg %s default = %#v, want %v", w.name, got.Default, d)
			}
		case bool:
			if lit, ok := got.Default.(*BoolLit); !ok || lit.Value != d {
				t.Errorf("arg %s default = %#v, want %v", w.name, got.Default, d)
			}
		}
	}
}

func TestParseArgWithoutDefaultIsNil(t *testing.T) {
	prog := mustParse(t, "instruction \"x\"\nargs { p: string }\nrun { print(p) }")
	if prog.Args[0].Default != nil {
		t.Fatalf("default = %#v, want nil", prog.Args[0].Default)
	}
}

// --- expressions ---------------------------------------------------------

func TestParseExpressionPrecedence(t *testing.T) {
	prog := mustParse(t, `instruction "x"
run {
    let a = 1 + 2 * 3
    let b = a == 1 && 2 != 3
    let c = !false
    let d = -5
    let e = (1 + 2) * 3
}
`)
	stmts := prog.Run.Stmts

	// 1 + (2 * 3)
	mul, ok := stmts[0].(*LetStmt).Value.(*BinaryExpr)
	if !ok || mul.Op != Plus {
		t.Fatalf("a = %#v", stmts[0])
	}
	if inner, ok := mul.Right.(*BinaryExpr); !ok || inner.Op != Star {
		t.Errorf("expected the right operand to be the multiplication")
	}

	// (a == 1) && (2 != 3)
	and, ok := stmts[1].(*LetStmt).Value.(*BinaryExpr)
	if !ok || and.Op != AndAnd {
		t.Fatalf("b = %#v", stmts[1])
	}
	if l, ok := and.Left.(*BinaryExpr); !ok || l.Op != Eq {
		t.Error("&& should bind looser than ==")
	}

	if u, ok := stmts[2].(*LetStmt).Value.(*UnaryExpr); !ok || u.Op != Bang {
		t.Errorf("c = %#v, want unary !", stmts[2])
	}
	if u, ok := stmts[3].(*LetStmt).Value.(*UnaryExpr); !ok || u.Op != Minus {
		t.Errorf("d = %#v, want unary -", stmts[3])
	}

	// Parentheses override: (1 + 2) * 3
	outer, ok := stmts[4].(*LetStmt).Value.(*BinaryExpr)
	if !ok || outer.Op != Star {
		t.Fatalf("e = %#v", stmts[4])
	}
	if inner, ok := outer.Left.(*BinaryExpr); !ok || inner.Op != Plus {
		t.Error("parentheses should force the addition to be the left operand")
	}
}

func TestParseComparisonChainAndCalls(t *testing.T) {
	prog := mustParse(t, `instruction "x"
run {
    if count >= 2 && count < 10 {
        print("in range")
    }
}
`)
	cond := prog.Run.Stmts[0].(*IfStmt).Cond.(*BinaryExpr)
	if cond.Op != AndAnd {
		t.Fatalf("cond op = %s", cond.Op)
	}
	if l := cond.Left.(*BinaryExpr); l.Op != Ge {
		t.Errorf("left op = %s, want >=", l.Op)
	}
	if r := cond.Right.(*BinaryExpr); r.Op != Lt {
		t.Errorf("right op = %s, want <", r.Op)
	}
}

func TestParseCallWithMultipleArgs(t *testing.T) {
	prog := mustParse(t, `instruction "x"
run {
    fn add(a: int, b: int) {
        print("sum")
    }
    add(1, 2)
}
`)
	call := prog.Run.Stmts[1].(*ExprStmt).X.(*CallExpr)
	if call.Fn != "add" || len(call.Args) != 2 {
		t.Fatalf("call = %+v", call)
	}
}

func TestParseElseIfChain(t *testing.T) {
	prog := mustParse(t, `instruction "x"
run {
    if a == 1 {
        print("one")
    } else if a == 2 {
        print("two")
    } else {
        print("other")
    }
}
`)
	outer := prog.Run.Stmts[0].(*IfStmt)
	if outer.Else == nil || len(outer.Else.Stmts) != 1 {
		t.Fatalf("else = %+v, want one nested if", outer.Else)
	}
	nested, ok := outer.Else.Stmts[0].(*IfStmt)
	if !ok {
		t.Fatalf("nested = %T, want *IfStmt", outer.Else.Stmts[0])
	}
	if nested.Else == nil {
		t.Error("the nested if should carry the final else")
	}
}

func TestParseElseOnNextLine(t *testing.T) {
	// The closing brace and `else` may be on separate lines.
	mustParse(t, `instruction "x"
run {
    if a {
        print("y")
    }
    else {
        print("z")
    }
}
`)
}

// --- opaque bodies -------------------------------------------------------

func TestParseExecBodyIsByteExact(t *testing.T) {
	body := "<?php\n\n// Application-specific credential update logic goes here.\n\n"
	prog := mustParse(t, "instruction \"x\"\nrun {\n    exec php <<("+body+")<<\n}\n")
	exec := prog.Run.Stmts[0].(*ExecStmt)
	if exec.Interpreter != "php" {
		t.Errorf("interpreter = %q", exec.Interpreter)
	}
	if string(exec.Body) != body {
		t.Fatalf("body = %q, want %q", exec.Body, body)
	}
}

func TestParseExecBodySurvivesNestedQuotes(t *testing.T) {
	// The body contains the delimiters and quotes of other languages; none of
	// it may be reinterpreted by nscript.
	safe := "\ncat <<'EOF'\n  $(( 1 + 1 )) \"nested\" 'quotes'\nEOF\n"
	prog := mustParse(t, "instruction \"x\"\nrun {\n  exec bash <<("+safe+")<<\n}\n")
	exec := prog.Run.Stmts[0].(*ExecStmt)
	if string(exec.Body) != safe {
		t.Fatalf("body = %q, want %q", exec.Body, safe)
	}
}

// --- errors --------------------------------------------------------------

func TestParseRequiresInstructionAndRun(t *testing.T) {
	wantErrContains(t, `run { print("x") }`, "missing an \"instruction\"")
	wantErrContains(t, `instruction "x"`, "missing a \"run\"")
	wantErrContains(t, `instruction ""`, "must not be empty")
	wantErrContains(t, "instruction \"\"\"\n\"\"\"\nrun { print(\"x\") }", "must not be empty")
}

func TestParseRejectsDuplicateSections(t *testing.T) {
	wantErrContains(t, "instruction \"a\"\ninstruction \"b\"\nrun { print(\"x\") }", "duplicate")
	wantErrContains(t, "instruction \"a\"\nrun { print(\"x\") }\nrun { print(\"y\") }", "duplicate")
	wantErrContains(t, "instruction \"a\"\nconfirm true\nconfirm false\nrun { print(\"x\") }", "duplicate")
	wantErrContains(t, "instruction \"a\"\nargs { q: string }\nargs { r: string }\nrun { print(\"x\") }", "duplicate")
}

func TestParseRejectsUnknownTopLevel(t *testing.T) {
	wantErrContains(t, "instruction \"a\"\nlet x = 1\nrun { print(\"x\") }", "top level")
}

func TestParseArgErrors(t *testing.T) {
	wantErrContains(t, `instruction "x"
args {
    p string
}
run { print("x") }`, "expected \":\"")

	wantErrContains(t, `instruction "x"
args {
    p: number
}
run { print("x") }`, "expected a type")

	wantErrContains(t, `instruction "x"
args {
    p: string
    p: int
}
run { print("x") }`, "duplicate argument")

	wantErrContains(t, `instruction "x"
args {
    p: int = "not an int"
}
run { print("x") }`, "is a string")

	wantErrContains(t, `instruction "x"
args {
    p: bool = 1
}
run { print("x") }`, "is an integer")

	wantErrContains(t, `instruction "x"
args {
    p: int = other
}
run { print("x") }`, "must be a literal")
}

func TestParseStatementErrors(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    print("a") print("b")
}
`, "expected a newline or \"}\" after the statement")

	wantErrContains(t, `instruction "x"
run {
    1 + 2
}
`, "an expression on its own has no effect")

	wantErrContains(t, `instruction "x"
run {
    let = 5
}
`, "expected a variable name")

	wantErrContains(t, `instruction "x"
run {
    print("unterminated
}
`, "unterminated string literal")
}

func TestParseUnterminatedBlock(t *testing.T) {
	wantErrContains(t, "instruction \"x\"\nrun {\n  print(\"y\")\n", "unterminated block")
	wantErrContains(t, "instruction \"x\"\nargs {\n  p: string\n", "unterminated")
	wantErrContains(t, "instruction \"x\"\nrun {\n  env {\n    A = 1\n", "unterminated")
}

func TestParseExecErrors(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    exec
}
`, "expected an interpreter")

	wantErrContains(t, `instruction "x"
run {
    exec bash
}
`, "expected a <<( ... )<< block")
}

func TestParseUnknownFunction(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    prnt("hello")
}
`, `unknown function "prnt"`)
}

func TestParseBuiltinArity(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    print()
}
`, "print takes exactly 1 argument")

	wantErrContains(t, `instruction "x"
run {
    print("a", "b")
}
`, "print takes exactly 1 argument")
}

func TestParseUserFunctionArity(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    fn greet(name: string) {
        print(name)
    }
    greet()
}
`, "greet takes exactly 1 argument")

	wantErrContains(t, `instruction "x"
run {
    fn pair(a: string, b: string = "b") {
        print(a)
    }
    pair()
}
`, "pair takes between 1 and 2 arguments")

	// The optional argument may be omitted.
	mustParse(t, `instruction "x"
run {
    fn pair(a: string, b: string = "b") {
        print(a)
    }
    pair("a")
}
`)
}

func TestParseDuplicateFunction(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    fn f() {
        print("a")
    }
    fn f() {
        print("b")
    }
}
`, "duplicate function")
}

func TestParseFunctionNameReserved(t *testing.T) {
	wantErrContains(t, `instruction "x"
run {
    fn print() {
        print("x")
    }
}
`, "builtin names are reserved")
}

func TestParseErrorCarriesPosition(t *testing.T) {
	_, err := Parse([]byte("instruction \"x\"\nrun {\n  let = 1\n}\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	var serr *Error
	if !errors.As(err, &serr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if serr.Pos.Line != 3 {
		t.Fatalf("error at line %d, want 3 (%v)", serr.Pos.Line, err)
	}
}

func TestParseCommentsEverywhere(t *testing.T) {
	prog := mustParse(t, `// a command
instruction "x" // trailing
// between sections
args {
    // inside args
    p: string // after a declaration
}
run {
    // inside run
    print("y") // after a statement
}
// at the end
`)
	if len(prog.Args) != 1 || prog.Args[0].Name != "p" {
		t.Fatalf("args = %+v", prog.Args)
	}
	if len(prog.Run.Stmts) != 1 {
		t.Fatalf("stmts = %d", len(prog.Run.Stmts))
	}
}

func TestParseCRLFSource(t *testing.T) {
	prog := mustParse(t, "instruction \"x\"\r\nargs {\r\n  p: string\r\n}\r\nrun {\r\n  print(p)\r\n}\r\n")
	if len(prog.Args) != 1 || len(prog.Run.Stmts) != 1 {
		t.Fatalf("prog = %+v", prog)
	}
}

func TestParseNestedBlocks(t *testing.T) {
	prog := mustParse(t, `instruction "x"
run {
    if a {
        if b {
            print("deep")
        }
    }
}
`)
	outer := prog.Run.Stmts[0].(*IfStmt)
	inner := outer.Then.Stmts[0].(*IfStmt)
	if _, ok := inner.Then.Stmts[0].(*ExprStmt); !ok {
		t.Fatalf("deepest statement = %T", inner.Then.Stmts[0])
	}
}
