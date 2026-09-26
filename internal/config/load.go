package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// Load reads the configuration file at path. An empty path means the default
// location.
//
// A missing file is not an error: it yields the zero Config, which is the
// state of a fresh install (D20).
//
// Unknown fields are rejected rather than ignored. A misspelled key in a
// hand-written configuration file is a mistake worth reporting, and silently
// ignoring it would leave the user wondering why their setting did nothing.
func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	// An empty file is a valid way to say "use the defaults".
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{Path: path}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	// A second value means the file holds more than one document, which is
	// almost always a truncated edit.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: %s: unexpected trailing content", path)
	}

	roots, err := expandRoots(cfg.CommandRoots)
	if err != nil {
		return Config{}, err
	}
	cfg.CommandRoots = roots
	cfg.Path = path
	return cfg, nil
}

func expandRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			// A blank entry would otherwise silently become the working
			// directory.
			continue
		}
		expanded, err := ExpandPath(r)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}
