// Package commands discovers .nsc files and exposes them as a registry.
//
// Discovery is deliberately tolerant: one unparsable command does not hide the
// rest. Failures are collected in Registry.Errors so the CLI can report them
// on stderr while still offering everything that did load.
package commands

import (
	"fmt"
	"sort"

	"github.com/mhs003/needless/internal/nscript"
)

// Extension is the nscript file extension (AGENTS.md §1).
const Extension = ".nsc"

// Command is one discovered .nsc file.
type Command struct {
	// ID is the path relative to its root, without the extension, using
	// forward slashes: "project/start".
	ID string

	// Path is the path of the source file as discovered.
	Path string

	// Program is the parsed script.
	Program *nscript.Program
}

// Instruction is the command's natural-language description. It is the only
// thing the model matches a prompt against.
func (c Command) Instruction() string { return c.Program.Instruction }

// Args are the command's declared argument slots, in source order.
func (c Command) Args() []nscript.Arg { return c.Program.Args }

// String renders the command for a human.
func (c Command) String() string { return c.ID }

// LoadError records a command that could not be loaded.
type LoadError struct {
	Path string
	Err  error
}

func (e LoadError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e LoadError) Unwrap() error { return e.Err }

// Registry is the set of commands available to the CLI.
type Registry struct {
	commands []Command
	errs     []LoadError
}

// Len reports how many commands loaded successfully.
func (r *Registry) Len() int { return len(r.commands) }

// Commands returns the loaded commands in a stable order (ascending ID).
// The order is stable so that the model's tool list does not shuffle between
// runs.
func (r *Registry) Commands() []Command {
	out := make([]Command, len(r.commands))
	copy(out, r.commands)
	return out
}

// Errors returns the commands that failed to load, in discovery order.
func (r *Registry) Errors() []LoadError {
	out := make([]LoadError, len(r.errs))
	copy(out, r.errs)
	return out
}

// Lookup finds a command by its ID.
func (r *Registry) Lookup(id string) (Command, bool) {
	for _, c := range r.commands {
		if c.ID == id {
			return c, true
		}
	}
	return Command{}, false
}

// IDs returns every command ID in order.
func (r *Registry) IDs() []string {
	out := make([]string, len(r.commands))
	for i, c := range r.commands {
		out[i] = c.ID
	}
	return out
}

func (r *Registry) sort() {
	sort.SliceStable(r.commands, func(i, j int) bool {
		return r.commands[i].ID < r.commands[j].ID
	})
}
