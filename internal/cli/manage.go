package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/mhs003/needless/internal/commands"
)

// manage handles the command-management modes (CLI spec §11).
func (a *app) manage(opt options) int {
	_, reg, roots, err := a.load()
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}
	a.reportLoadErrors(reg)

	switch opt.mode {
	case modeList:
		return a.list(reg, roots)
	case modeNew:
		return a.newCommand(roots, opt.operand)
	case modeEdit:
		return a.editCommand(reg, opt.operand)
	}
	return ExitUsage
}

// list prints the available commands in the form the spec sketches.
func (a *app) list(reg *commands.Registry, roots []string) int {
	if reg.Len() == 0 {
		// An empty listing is a successful query, like `ls` on an empty
		// directory; the hint goes to stderr so stdout stays pipeable (D45).
		fmt.Fprintf(a.stderr, "n: no commands found in %s\n", strings.Join(roots, ", "))
		fmt.Fprintf(a.stderr, "Create one with 'n --new-command'.\n")
		return ExitOK
	}
	for _, c := range reg.Commands() {
		fmt.Fprintf(a.stdout, "%s\n    %s\n\n", c.ID, summary(c.Instruction()))
	}
	return ExitOK
}

// newCommand creates a command file from a minimal template.
func (a *app) newCommand(roots []string, operand string) int {
	if len(roots) == 0 {
		fmt.Fprintln(a.stderr, "n: no command root is configured")
		return ExitFailure
	}

	id := strings.TrimSuffix(strings.TrimSpace(operand), commands.Extension)
	if id == "" {
		fmt.Fprint(a.stderr, "Command id (for example git/status): ")
		line, err := a.readLine()
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(a.stderr)
			fmt.Fprintln(a.stderr, "n: no command id given")
			return ExitUsage
		}
		id = strings.TrimSuffix(strings.TrimSpace(line), commands.Extension)
	}

	if err := validateID(id); err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitUsage
	}

	root := roots[0]
	path := filepath.Join(root, filepath.FromSlash(id)+commands.Extension)
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(a.stderr, "n: %s already exists\n", path)
		return ExitFailure
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}
	if err := os.WriteFile(path, []byte(commandTemplate), 0o644); err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}

	fmt.Fprintf(a.stderr, "Created %s\n", path)
	a.openEditor(path)
	return ExitOK
}

// editCommand opens an existing command, asking which one if needed.
func (a *app) editCommand(reg *commands.Registry, operand string) int {
	id := strings.TrimSuffix(strings.TrimSpace(operand), commands.Extension)

	if id == "" {
		cmds := reg.Commands()
		if len(cmds) == 0 {
			fmt.Fprintln(a.stderr, "n: there are no commands to edit")
			return ExitFailure
		}
		for i, c := range cmds {
			fmt.Fprintf(a.stderr, "  %d) %s\n", i+1, c.ID)
		}
		fmt.Fprint(a.stderr, "Which command? ")
		line, err := a.readLine()
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(a.stderr)
			fmt.Fprintln(a.stderr, "n: nothing chosen")
			return ExitFailure
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(cmds) {
			fmt.Fprintf(a.stderr, "n: %q is not one of the listed commands\n", strings.TrimSpace(line))
			return ExitUsage
		}
		fmt.Fprintf(a.stderr, "%s\n", cmds[n-1].Path)
		a.openEditor(cmds[n-1].Path)
		return ExitOK
	}

	cmd, ok := reg.Lookup(id)
	if !ok {
		fmt.Fprintf(a.stderr, "n: no command named %q\n", id)
		return ExitFailure
	}
	fmt.Fprintf(a.stderr, "%s\n", cmd.Path)
	a.openEditor(cmd.Path)
	return ExitOK
}

// openEditor hands the file to $VISUAL or $EDITOR.
//
// When neither is set the path is already on stderr, so the user can open it
// themselves; that is not an error (D46).
func (a *app) openEditor(path string) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		// Say where the file is, then how to make this automatic. The path
		// itself was already reported by the caller that created or found it.
		fmt.Fprintln(a.stderr, "Set $EDITOR to open it automatically.")
		return
	}

	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin = a.stdin
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(a.stderr, "n: %s exited with %v\n", parts[0], err)
	}
}

// validateID checks a command id before it becomes a path.
//
// This is the one place a user-supplied string becomes a filesystem path, so
// it is validated rather than trusted: no absolute paths, no `.` or `..`
// segments, and only characters that are safe in a filename. Each segment must
// begin with a letter or digit, which rules out names like "-" that would be
// mistaken for an option.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("a command id is required")
	}
	if strings.HasPrefix(id, "/") || strings.HasSuffix(id, "/") {
		return fmt.Errorf("command id %q must not start or end with %q", id, "/")
	}
	for _, part := range strings.Split(id, "/") {
		switch part {
		case "", ".", "..":
			return fmt.Errorf("command id %q contains the invalid segment %q", id, part)
		}
		if !isIDStart(rune(part[0])) {
			return fmt.Errorf("command id %q: each part must start with a letter or digit", id)
		}
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
				continue
			}
			return fmt.Errorf("command id %q contains %q; use letters, digits, '-', '_', '.' and '/'", id, r)
		}
	}
	return nil
}

func isIDStart(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// commandTemplate is the starting point for `n --new-command`.
//
// It uses only constructs the language actually has, so a freshly created file
// parses and lists without edits.
const commandTemplate = `instruction """
Describe what this command does, in the words a user would use to ask for it.
This text is what the model matches a prompt against.
"""

confirm false

args {
    // name: string
    // count: int = 1
}

run {
    print("Replace this with what the command should actually do.")
}
`
