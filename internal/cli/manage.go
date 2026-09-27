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
	case modeRemove:
		return a.removeCommand(reg, roots, opt.operand)
	case modeShow:
		return a.showCommand(reg, opt.operand)
	}
	return ExitUsage
}

// resolveCommand turns an id into a command, or asks which one when no id was
// given. verb names the action in the message the user sees.
//
// The returned code is ExitOK when a command was chosen; any other value is the
// exit code to return directly.
func (a *app) resolveCommand(reg *commands.Registry, operand, verb string) (commands.Command, int) {
	id := strings.TrimSuffix(strings.TrimSpace(operand), commands.Extension)
	if id != "" {
		cmd, ok := reg.Lookup(id)
		if !ok {
			fmt.Fprintf(a.stderr, "n: no command named %q\n", id)
			return commands.Command{}, ExitFailure
		}
		return cmd, ExitOK
	}

	// No id given, so offer a selection.
	cmds := reg.Commands()
	if len(cmds) == 0 {
		fmt.Fprintf(a.stderr, "n: there are no commands to %s\n", verb)
		return commands.Command{}, ExitFailure
	}
	for i, c := range cmds {
		fmt.Fprintf(a.stderr, "  %d) %s\n", i+1, c.ID)
	}
	fmt.Fprint(a.stderr, "Which command? ")
	line, err := a.readLine()
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(a.stderr)
		fmt.Fprintln(a.stderr, "n: nothing chosen")
		return commands.Command{}, ExitFailure
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(cmds) {
		fmt.Fprintf(a.stderr, "n: %q is not one of the listed commands\n", strings.TrimSpace(line))
		return commands.Command{}, ExitUsage
	}
	return cmds[n-1], ExitOK
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
	cmd, code := a.resolveCommand(reg, operand, "edit")
	if code != ExitOK {
		return code
	}
	fmt.Fprintf(a.stderr, "%s\n", cmd.Path)
	a.openEditor(cmd.Path)
	return ExitOK
}

// showCommand prints a command's source. It is a read, so nothing is asked.
func (a *app) showCommand(reg *commands.Registry, operand string) int {
	cmd, code := a.resolveCommand(reg, operand, "show")
	if code != ExitOK {
		return code
	}

	body, err := os.ReadFile(cmd.Path)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}
	// The source is the requested output, so it goes to stdout and stays
	// pipeable (D47). The path is a diagnostic and goes to stderr. The bytes
	// are passed through exactly as stored, like `cat`.
	fmt.Fprintf(a.stderr, "%s\n", cmd.Path)
	if _, err := a.stdout.Write(body); err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}
	return ExitOK
}

// removeCommand deletes a command, after asking.
//
// This is the only place Needless destroys something the user wrote, so it is
// deliberate at every step: the path is checked to be inside a command root
// rather than trusted, the confirmation defaults to no, and the file is
// removed rather than the directory, so nothing else goes with it.
func (a *app) removeCommand(reg *commands.Registry, roots []string, operand string) int {
	cmd, code := a.resolveCommand(reg, operand, "remove")
	if code != ExitOK {
		return code
	}

	if !withinRoots(cmd.Path, roots) {
		fmt.Fprintf(a.stderr, "n: refusing to remove %s: it is not inside a command root\n", cmd.Path)
		return ExitFailure
	}

	fmt.Fprintf(a.stderr, "%s\n", cmd.Path)
	if !a.confirm("Remove this command? [y/N] ") {
		fmt.Fprintf(a.stderr, "n: %s was not removed\n", cmd.ID)
		return ExitFailure
	}
	if err := os.Remove(cmd.Path); err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}

	fmt.Fprintf(a.stderr, "Removed %s\n", cmd.ID)
	return ExitOK
}

// withinRoots reports whether path lies inside one of the command roots.
//
// A command id is validated when it is created (validateID), but discovery
// takes whatever the filesystem holds, so this re-checks against the roots
// actually in use before deleting anything. The path must be a descendant, not
// the root itself: `..` and `.` are refused.
func withinRoots(path string, roots []string) bool {
	clean := filepath.Clean(path)
	for _, root := range roots {
		rel, err := filepath.Rel(filepath.Clean(root), clean)
		if err != nil {
			continue
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
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
