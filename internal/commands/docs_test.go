package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhs003/needless/internal/nscript"
)

// The guides under docs/ are the material a user actually copies from, so a
// command printed there that does not parse is worse than no example at all —
// and it is exactly the kind of thing that rots silently once written.
//
// A fenced block tagged `nsc` that declares both `instruction` and `run` is
// presented as a complete command and must parse. Blocks that show one section
// in isolation — a bare `args { ... }`, or `confirm true` — are fragments and
// are skipped, which is why the guide keeps the syntax-heavy examples whole.

// docsDir walks up to the module root and returns docs/.
func docsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "docs")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find docs/ above %s", dir)
		}
		dir = parent
	}
}

func TestDocumentationCommandsParse(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(docsDir(t), "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no documentation found under docs/")
	}

	checked := 0
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range nscFences(string(body)) {
			if !strings.Contains(block, "instruction") || !strings.Contains(block, "run {") {
				continue // a fragment, not a complete command
			}
			checked++
			if _, err := nscript.Parse([]byte(block)); err != nil {
				t.Errorf("%s, nsc block %d does not parse: %v\n%s",
					filepath.Base(file), i+1, err, block)
			}
		}
	}

	// If the extraction silently stopped finding anything, this guard would
	// pass while checking nothing at all.
	if checked == 0 {
		t.Fatal("no complete commands were found to check; the extraction is broken")
	}
	t.Logf("checked %d complete commands across %d files", checked, len(files))
}

// nscFences returns the contents of every fenced block whose info string is
// exactly "nsc".
func nscFences(body string) []string {
	var (
		out     []string
		current []string
		in      bool
	)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && trimmed == "```nsc":
			in, current = true, nil
		case in && trimmed == "```":
			out = append(out, strings.Join(current, "\n")+"\n")
			in = false
		case in:
			current = append(current, line)
		}
	}
	return out
}
