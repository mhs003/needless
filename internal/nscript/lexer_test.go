package nscript

import (
	"errors"
	"strings"
	"testing"
)

// kinds is a compact helper for asserting a token stream.
func kinds(toks []Token) []Kind {
	out := make([]Kind, len(toks))
	for i, t := range toks {
		out[i] = t.Kind
	}
	return out
}

func TestLexSimpleCommand(t *testing.T) {
	src := `instruction """Start the server."""
args {
    project: string
    port: int = 8000
}
confirm false
run {
    print("starting")
}
`
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatalf("Lex: %v", err)
	}

	var got []Kind
	for _, tk := range toks {
		if tk.Kind != Newline {
			got = append(got, tk.Kind)
		}
	}

	want := []Kind{
		KwInstruction, RawString,
		KwArgs, LBrace,
		Ident, Colon, KwString,
		Ident, Colon, KwInt, Assign, Int,
		RBrace,
		KwConfirm, KwFalse,
		KwRun, LBrace,
		KwPrint, LParen, String, RParen,
		RBrace,
		EOF,
	}
	if len(got) != len(want) {
		t.Fatalf("token count = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestLexKeepsLiteralText(t *testing.T) {
	src := `args { port: int = 8000 rate: float = 1.5 name: string = "x" }`
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var ints, floats, strs []string
	for _, tk := range toks {
		switch tk.Kind {
		case Int:
			ints = append(ints, tk.Text)
		case Float:
			floats = append(floats, tk.Text)
		case String:
			strs = append(strs, tk.Text)
		}
	}
	if len(ints) != 1 || ints[0] != "8000" {
		t.Errorf("ints = %v", ints)
	}
	if len(floats) != 1 || floats[0] != "1.5" {
		t.Errorf("floats = %v", floats)
	}
	if len(strs) != 1 || strs[0] != "x" {
		t.Errorf("strings = %v", strs)
	}
}

func TestLexStringEscapes(t *testing.T) {
	toks, err := Lex([]byte(`run { print("a\tb\nc\"d\\e") }`))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, tk := range toks {
		if tk.Kind == String {
			got = tk.Text
		}
	}
	want := "a\tb\nc\"d\\e"
	if got != want {
		t.Fatalf("decoded string = %q, want %q", got, want)
	}
}

func TestLexCommentsAreSkipped(t *testing.T) {
	src := "// leading comment\nrun { // trailing\n    print(\"x\") // after\n}\n"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var got []Kind
	for _, tk := range toks {
		if tk.Kind != Newline {
			got = append(got, tk.Kind)
		}
	}
	want := []Kind{KwRun, LBrace, KwPrint, LParen, String, RParen, RBrace, EOF}
	if len(got) != len(want) {
		t.Fatalf("got %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestLexTripleQuotedIsVerbatim(t *testing.T) {
	// The body must survive byte-for-byte: no trimming, no re-indenting,
	// no escape processing.
	body := "\nChange the admin credentials.\n\nUse this when the user asks.\n"
	toks, err := Lex([]byte("instruction \"\"\"" + body + "\"\"\""))
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Kind != KwInstruction {
		t.Fatalf("first token = %s", toks[0].Kind)
	}
	if toks[1].Kind != RawString {
		t.Fatalf("second token = %s", toks[1].Kind)
	}
	if toks[1].Text != body {
		t.Fatalf("triple-quoted body was altered:\n got %q\nwant %q", toks[1].Text, body)
	}
	// Backslashes must not be treated as escapes.
	toks, err = Lex([]byte("instruction \"\"\"C:\\path\\n\"\"\""))
	if err != nil {
		t.Fatal(err)
	}
	if toks[1].Text != `C:\path\n` {
		t.Fatalf("escapes were processed: %q", toks[1].Text)
	}
}

func TestLexRawBlockIsVerbatim(t *testing.T) {
	body := "\nset -e\n\ncd \"$project\"\nif [ -f x ]; then\n    echo 'a' # note\nfi\n\n"
	src := "run {\n    exec bash <<(" + body + ")<<\n}\n"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	var got *Token
	for i := range toks {
		if toks[i].Kind == RawBlock {
			got = &toks[i]
		}
	}
	if got == nil {
		t.Fatalf("no RawBlock token produced; tokens: %v", kinds(toks))
	}
	if got.Text != body {
		t.Fatalf("block body was altered:\n got %q\nwant %q", got.Text, body)
	}
}

func TestLexRawBlockKeepsShellMetacharacters(t *testing.T) {
	// Everything a shell cares about must pass through untouched.
	body := `echo "$HOME" && (cd /tmp; ls) | grep -v 'x' ; echo ` + "`date`" + "\n"
	src := "exec bash <<(" + body + ")<<"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 4 {
		t.Fatalf("tokens = %v, want [exec ident rawblock eof]", kinds(toks))
	}
	if toks[1].Text != "bash" {
		t.Errorf("interpreter = %q", toks[1].Text)
	}
	if toks[2].Text != body {
		t.Fatalf("body altered:\n got %q\nwant %q", toks[2].Text, body)
	}
}

func TestLexExecInterpreterForms(t *testing.T) {
	cases := map[string]string{
		`exec bash <<(x)<<`:         "bash",
		`exec /bin/bash <<(x)<<`:    "/bin/bash",
		`exec /usr/bin/php <<(x)<<`: "/usr/bin/php",
		`exec python <<(x)<<`:       "python",
		`exec bash<<(x)<<`:          "bash",
		"exec /bin/sh\n<<(x)<<":     "", // newline before the block: not an interpreter
		`exec python3.12 <<(x)<<`:   "python3.12",
	}
	for src, wantInterp := range cases {
		toks, err := Lex([]byte(src))
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if wantInterp == "" {
			continue
		}
		if toks[0].Kind != KwExec {
			t.Errorf("%q: first token = %s", src, toks[0].Kind)
			continue
		}
		if toks[1].Text != wantInterp {
			t.Errorf("%q: interpreter = %q, want %q", src, toks[1].Text, wantInterp)
		}
	}
}

func TestLexExecWithoutInterpreterIsIllegal(t *testing.T) {
	_, err := Lex([]byte("exec <<(x)<<"))
	if err == nil {
		t.Fatal("expected an error for a missing interpreter")
	}
	if !strings.Contains(err.Error(), "interpreter") {
		t.Fatalf("error = %v, want it to mention the interpreter", err)
	}
}

func TestLexOperators(t *testing.T) {
	toks, err := Lex([]byte(`if a == b && c != d || e <= f { }`))
	if err != nil {
		t.Fatal(err)
	}
	var got []Kind
	for _, tk := range toks {
		if tk.Kind != Newline {
			got = append(got, tk.Kind)
		}
	}
	want := []Kind{
		KwIf, Ident, Eq, Ident, AndAnd, Ident, Ne, Ident, OrOr, Ident, Le, Ident,
		LBrace, RBrace, EOF,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestLexGreedyOperators(t *testing.T) {
	// `<=` must not lex as `<` `=`; same for the other two-character forms.
	for src, want := range map[string]Kind{
		"a <= b": Le,
		"a >= b": Ge,
		"a == b": Eq,
		"a != b": Ne,
		"a && b": AndAnd,
		"a || b": OrOr,
	} {
		toks, err := Lex([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if toks[1].Kind != want {
			t.Errorf("%q: operator = %s, want %s", src, toks[1].Kind, want)
		}
	}
}

func TestLexPositions(t *testing.T) {
	src := "run {\n    let x = 1\n}\n"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// run(1:1) {(1:5) nl let(2:5) x ident(2:9) =(2:11) 1(2:13) nl }(3:1)
	for _, tk := range toks {
		switch tk.Text {
		case "run":
			if tk.Pos.Line != 1 || tk.Pos.Column != 1 {
				t.Errorf("run at %s, want 1:1", tk.Pos)
			}
		case "let":
			if tk.Pos.Line != 2 || tk.Pos.Column != 5 {
				t.Errorf("let at %s, want 2:5", tk.Pos)
			}
		case "x":
			if tk.Pos.Line != 2 || tk.Pos.Column != 9 {
				t.Errorf("x at %s, want 2:9", tk.Pos)
			}
		}
	}
}

func TestLexPositionsAfterRawBlock(t *testing.T) {
	// A multi-line opaque block must still advance the line counter, or every
	// later error points at the wrong place.
	src := "run {\n  exec bash <<(\nline\nline\n)<<\n  print(\"after\")\n}\n"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range toks {
		if tk.Text == "after" {
			if tk.Pos.Line != 6 {
				t.Fatalf("string after block at line %d, want 6", tk.Pos.Line)
			}
			return
		}
	}
	t.Fatal("did not find the trailing string token")
}

func TestLexCRLF(t *testing.T) {
	toks, err := Lex([]byte("run {\r\n  print(\"x\")\r\n}\r\n"))
	if err != nil {
		t.Fatalf("CRLF source failed to lex: %v", err)
	}
	var got []Kind
	for _, tk := range toks {
		if tk.Kind != Newline {
			got = append(got, tk.Kind)
		}
	}
	want := []Kind{KwRun, LBrace, KwPrint, LParen, String, RParen, RBrace, EOF}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLexEmptyAndWhitespaceOnly(t *testing.T) {
	for _, src := range []string{"", "   \t\r\n", "// just a comment", "\n\n\n"} {
		toks, err := Lex([]byte(src))
		if err != nil {
			t.Errorf("Lex(%q): %v", src, err)
			continue
		}
		last := toks[len(toks)-1]
		if last.Kind != EOF {
			t.Errorf("Lex(%q): last token = %s, want EOF", src, last.Kind)
		}
		for _, tk := range toks[:len(toks)-1] {
			if tk.Kind != Newline {
				t.Errorf("Lex(%q): unexpected token %s", src, tk.Kind)
			}
		}
	}
}

func TestLexErrors(t *testing.T) {
	cases := map[string]string{
		`print("unterminated)`:   "unterminated string literal",
		"print(\"multi\nline\")": "unterminated string literal",
		`print("bad \q escape")`: "unknown escape",
		`instruction """open`:    "unterminated triple-quoted",
		`exec bash <<(open`:      "unterminated external script block",
		`run { $ }`:              "unexpected character",
	}
	for src, wantSub := range cases {
		_, err := Lex([]byte(src))
		if err == nil {
			t.Errorf("%q: expected an error", src)
			continue
		}
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("%q: error = %v, want it to contain %q", src, err, wantSub)
		}
	}
}

func TestLexErrorCarriesPosition(t *testing.T) {
	_, err := Lex([]byte("run {\n  print(\"oops)\n}\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	var serr *Error
	if !errors.As(err, &serr) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if serr.Pos.Line != 2 {
		t.Fatalf("error at line %d, want 2 (%v)", serr.Pos.Line, err)
	}
	if !strings.HasPrefix(serr.Error(), serr.Pos.String()) {
		t.Fatalf("error string %q does not start with the position", serr.Error())
	}
}

func TestLexKeywordBoundaries(t *testing.T) {
	// Keywords must be whole words: `runner` is an identifier, not `run` + `ner`.
	toks, err := Lex([]byte("runner lets prints"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range toks {
		if tk.Kind == EOF {
			continue
		}
		if tk.Kind != Ident {
			t.Errorf("token %q = %s, want identifier", tk.Text, tk.Kind)
		}
	}
}

func TestLexNumbersDoNotEatDots(t *testing.T) {
	toks, err := Lex([]byte("1.foo"))
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(toks)
	want := []Kind{Int, Dot, Ident, EOF}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if toks[0].Text != "1" || toks[2].Text != "foo" {
		t.Fatalf("texts = %q %q", toks[0].Text, toks[2].Text)
	}
}

// isKeyword relies on the keyword constants being contiguous. This pins that
// invariant, so inserting a non-keyword kind into the middle of the block
// fails here rather than silently making isKeyword lie.
func TestKeywordsAreContiguous(t *testing.T) {
	inRange := map[Kind]bool{}
	for k := KwInstruction; k <= KwBool; k++ {
		inRange[k] = true
	}
	fromMap := map[Kind]bool{}
	for name, k := range keywords {
		fromMap[k] = true
		if !inRange[k] {
			t.Errorf("keyword %q maps to %s, outside KwInstruction..KwBool", name, k)
		}
		if k.String() == "" {
			t.Errorf("keyword %q has no display name", name)
		}
	}
	if len(fromMap) != len(inRange) {
		t.Fatalf("the range holds %d kinds but the keywords map has %d entries", len(inRange), len(fromMap))
	}
}
