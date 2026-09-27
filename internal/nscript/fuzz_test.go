package nscript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse drives the lexer and parser with arbitrary bytes.
//
// Everything in a .nsc file is hand-written by a user, so a panic here would be
// a crash on someone's typo rather than on a hostile input — which makes it
// worth finding. Beyond not panicking, the invariants worth holding are that a
// nil error always comes with a usable Program, and that a failure always
// carries a position, because "parse error" with no location is not actionable.
func FuzzParse(f *testing.F) {
	// Whatever the shipped examples contain is, by definition, valid input.
	seedExamples(f)

	// Shapes that tend to break a hand-written parser.
	seeds := []string{
		"",
		" ",
		"\n\n\n",
		"instruction",
		`instruction "unterminated`,
		"instruction \"\"\"unterminated\n",
		"instruction \"\"\"\nok\n\"\"\"\nrun { }",
		"instruction \"\"\"\nok\n\"\"\"\nrun { print(\"x\") }",
		"args {",
		"args { }",
		"args { x: }",
		"args { : string }",
		"args { x: string = }",
		"args { x: nonexistent }",
		"run {",
		"run { }",
		"run { exec",
		"run { exec bash <<(",
		"run { exec bash <<(echo hi)<< }",
		"run { exec bash <<(\n)<<\n}",
		"run { exec bash <<()<<  }",
		")<<",
		"run { if }",
		"run { if true { } }",
		"run { if true { } else { } }",
		"run { if true { } else if false { } }",
		"run { fn }",
		"run { fn f() { } }",
		"run { fn f(a: string) { return a } }",
		"run { let }",
		"run { let x = }",
		"run { let x = \"\\q\" }",
		"run { error() }",
		"run { print(unknown()) }",
		"confirm",
		"confirm maybe",
		"confirm true",
		"// only a comment",
		"run { print(\"a\") } // trailing",
		"\x00\x01\x02",
		"instruction \"x\"\nrun { print(\"y\") }\x00",
		string([]byte{0xff, 0xfe, 0xfd}),
		"run { " + repeat("if true { ", 200) + repeat("}", 200) + " }",
		"args {\n" + repeat("a: string\n", 500) + "}",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		prog, err := Parse(src)
		if err != nil {
			// A failure must say where it happened, or the user cannot act on
			// it. Anything else escaping as a plain error is a bug too.
			var perr *Error
			if !errors.As(err, &perr) {
				t.Fatalf("Parse returned a non-positional error for %q: %v", src, err)
			}
			if prog != nil {
				t.Fatalf("Parse returned both a program and an error for %q", src)
			}
			return
		}
		if prog == nil {
			t.Fatalf("Parse returned a nil program and a nil error for %q", src)
		}

		// A program that parsed must be usable: the three sections the rest of
		// the system depends on have to be in a sane state.
		if prog.Instruction == "" {
			t.Fatalf("parsed %q with an empty instruction, which Parse should reject", src)
		}
		if prog.Run == nil {
			t.Fatalf("parsed %q with no run block, which Parse should reject", src)
		}
	})
}

// seedExamples adds every shipped example as a seed, so the corpus starts from
// input that is known to be valid.
func seedExamples(f *testing.F) {
	f.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	// internal/nscript -> the module root.
	root := filepath.Dir(filepath.Dir(dir))
	dir = filepath.Join(root, "examples", "commands")

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".nsc" {
			return nil
		}
		if body, err := os.ReadFile(path); err == nil {
			f.Add(body)
		}
		return nil
	})
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
