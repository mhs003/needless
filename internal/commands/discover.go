package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mhs003/needless/internal/nscript"
)

// Discover walks every root and parses the .nsc files it finds.
//
// Roots are searched in order. When two roots hold the same command ID the
// earlier root wins, so a user's own directory can shadow a shared one.
//
// A root that does not exist is not an error: a fresh install has no commands
// yet. A file that fails to parse is recorded in Errors and skipped, so one
// broken script never hides the rest.
//
// Where the roots come from is the config package's business; discovery only
// walks what it is given.
func Discover(roots ...string) *Registry {
	reg := &Registry{}
	seen := make(map[string]bool)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if err := walkRoot(reg, seen, filepath.Clean(root)); err != nil {
			reg.errs = append(reg.errs, LoadError{Path: root, Err: err})
		}
	}
	reg.sort()
	return reg
}

func walkRoot(reg *Registry, seen map[string]bool, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A missing root is the ordinary state of a fresh install.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			reg.errs = append(reg.errs, LoadError{Path: path, Err: err})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			// Never skip the root itself, even if its name starts with a dot.
			if path != root && isHidden(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}

		name := d.Name()
		if isHidden(name) || !strings.HasSuffix(name, Extension) {
			return nil
		}
		load(reg, seen, root, path)
		return nil
	})
}

func load(reg *Registry, seen map[string]bool, root, path string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		reg.errs = append(reg.errs, LoadError{Path: path, Err: err})
		return
	}
	id := strings.TrimSuffix(filepath.ToSlash(rel), Extension)
	if seen[id] {
		// An earlier root already provided this command.
		return
	}

	src, err := os.ReadFile(path)
	if err != nil {
		reg.errs = append(reg.errs, LoadError{Path: path, Err: err})
		return
	}
	prog, err := nscript.Parse(src)
	if err != nil {
		reg.errs = append(reg.errs, LoadError{Path: path, Err: err})
		return
	}

	seen[id] = true
	reg.commands = append(reg.commands, Command{ID: id, Path: path, Program: prog})
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }
