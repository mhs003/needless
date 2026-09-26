// Package config holds Needless' user configuration: where commands live and
// what to do when a prompt matches nothing.
//
// The file format is JSON. CLI spec §5 leaves the configuration format and
// location implementation-defined, and JSON keeps the project on the standard
// library with no third-party dependency (D22).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirName is the per-user directory Needless keeps its state in.
	DirName = ".needless"

	// FileName is the configuration file inside DirName.
	FileName = "config.json"

	// CommandsDirName is the default command directory inside DirName
	// (CLI spec §10).
	CommandsDirName = "commands"

	// ModelsDirName is the directory the model archive is looked for in.
	ModelsDirName = "models"

	// WeightsFileName is the model archive's conventional name.
	WeightsFileName = "needle3.cact"
)

// Dir returns the per-user state directory, ~/.needless.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, DirName), nil
}

// DefaultPath returns the configuration file's default location,
// ~/.needless/config.json.
func DefaultPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// DefaultCommandRoots returns the search path used when the configuration file
// does not name one: ~/.needless/commands.
func DefaultCommandRoots() ([]string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(dir, CommandsDirName)}, nil
}

// Config is the user's configuration.
type Config struct {
	// CommandRoots are searched in order, and the first root to provide a
	// command ID wins. An empty list means the default root.
	CommandRoots []string `json:"command_roots"`

	// Fallback is the ID of the command to run when no command matches the
	// prompt. Empty means there is no fallback, and an unmatched prompt
	// produces nothing rather than inventing a command (CLI spec §4, §5).
	Fallback string `json:"fallback"`

	// Weights is the .cact model archive. Empty means DefaultWeightsPath
	// searches the usual places.
	Weights string `json:"weights"`

	// Engine is the libneedle shared library. Empty means the binding's own
	// discovery order.
	Engine string `json:"engine"`

	// System holds environment facts handed to the model, in the
	// "date: ...; locale: ..." form. Empty means Needless supplies a date
	// fact, mirroring the reference Python binding (D44).
	System string `json:"system"`

	// Path records where the configuration was read from, or is empty when
	// the defaults were used. It is not part of the file.
	Path string `json:"-"`
}

// Roots returns the effective command roots: the configured ones, or the
// default when the configuration names none.
func (c Config) Roots() ([]string, error) {
	if len(c.CommandRoots) > 0 {
		return c.CommandRoots, nil
	}
	return DefaultCommandRoots()
}

// DefaultWeightsPath searches for the model archive, in this order:
//
//  1. <executable dir>/models/needle3.cact
//  2. <executable dir>/.needless/models/needle3.cact
//  3. <working dir>/models/needle3.cact
//  4. ~/.needless/models/needle3.cact
//
// The working directory is checked so that `go run ./cmd/n` from the repo root
// finds models/ in a checkout.
func DefaultWeightsPath() (string, bool) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, ModelsDirName, WeightsFileName),
			filepath.Join(dir, DirName, ModelsDirName, WeightsFileName),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, ModelsDirName, WeightsFileName))
	}
	if dir, err := Dir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, ModelsDirName, WeightsFileName))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return "", false
}

// ExpandPath expands a leading ~ and cleans the result. Relative paths are
// left relative; the caller resolves them.
func ExpandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: expand %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return filepath.Clean(p), nil
}
