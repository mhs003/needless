package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// clearHome points HOME at a temp directory so tests do not touch the real one.
func clearHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads $HOME on unix; on Windows it needs USERPROFILE.
	t.Setenv("USERPROFILE", home)
	return home
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "config.json")

	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("a missing config file should not be an error: %v", err)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty when nothing was read", cfg.Path)
	}
	if cfg.Fallback != "" || len(cfg.CommandRoots) != 0 {
		t.Errorf("cfg = %+v, want the zero value", cfg)
	}

	home := clearHome(t)
	roots, err := cfg.Roots()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, DirName, CommandsDirName)}
	if !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots() = %v, want %v", roots, want)
	}
}

func TestLoadEmptyFileYieldsDefaults(t *testing.T) {
	for _, body := range []string{"", "   \n\t\n"} {
		path := write(t, "config.json", body)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if cfg.Fallback != "" || len(cfg.CommandRoots) != 0 {
			t.Errorf("body %q gave %+v, want the zero value", body, cfg)
		}
		if cfg.Path != path {
			t.Errorf("Path = %q, want %q", cfg.Path, path)
		}
	}
}

func TestLoadFullConfig(t *testing.T) {
	path := write(t, "config.json", `{
		"command_roots": ["/srv/shared/commands", "/home/me/commands"],
		"fallback": "default"
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fallback != "default" {
		t.Errorf("Fallback = %q, want %q", cfg.Fallback, "default")
	}
	want := []string{"/srv/shared/commands", "/home/me/commands"}
	if !reflect.DeepEqual(cfg.CommandRoots, want) {
		t.Errorf("CommandRoots = %v, want %v", cfg.CommandRoots, want)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
}

func TestLoadExpandsTilde(t *testing.T) {
	home := clearHome(t)
	path := write(t, "config.json", `{"command_roots": ["~/mine", "~"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, "mine"), home}
	if !reflect.DeepEqual(cfg.CommandRoots, want) {
		t.Errorf("CommandRoots = %v, want %v", cfg.CommandRoots, want)
	}
}

func TestLoadDropsBlankRoots(t *testing.T) {
	path := write(t, "config.json", `{"command_roots": ["/a", "   ", "", "/b"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// A blank entry would otherwise resolve to the working directory.
	want := []string{"/a", "/b"}
	if !reflect.DeepEqual(cfg.CommandRoots, want) {
		t.Errorf("CommandRoots = %v, want %v", cfg.CommandRoots, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := write(t, "config.json", `{"command_root": ["/a"]}`) // typo: missing plural

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
	if !strings.Contains(err.Error(), "command_root") {
		t.Errorf("error = %v, want it to name the unknown field", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	for _, body := range []string{"{", `{"fallback": }`, "not json at all", `{"fallback": 5}`} {
		path := write(t, "config.json", body)
		if _, err := Load(path); err == nil {
			t.Errorf("Load(%q) should have failed", body)
		}
	}
}

func TestLoadRejectsTrailingContent(t *testing.T) {
	path := write(t, "config.json", `{"fallback": "a"} {"fallback": "b"}`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for two documents in one file")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error = %v, want it to mention trailing content", err)
	}
}

func TestLoadReportsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; file permissions are not enforced")
	}
	path := write(t, "config.json", `{}`)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	// Load wraps with %w, so the permission cause must survive for callers
	// that want to branch on it.
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want it to wrap fs.ErrPermission", err)
	}
}

func TestConfigRootsPrefersConfigured(t *testing.T) {
	clearHome(t)
	cfg := Config{CommandRoots: []string{"/configured"}}

	roots, err := cfg.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roots, []string{"/configured"}) {
		t.Errorf("Roots() = %v", roots)
	}
}

func TestPaths(t *testing.T) {
	home := clearHome(t)

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, DirName); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}

	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, DirName, FileName); path != want {
		t.Errorf("DefaultPath() = %q, want %q", path, want)
	}

	roots, err := DefaultCommandRoots()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, DirName, CommandsDirName)}; !reflect.DeepEqual(roots, want) {
		t.Errorf("DefaultCommandRoots() = %v, want %v", roots, want)
	}
}

func TestLoadDefaultPathWhenEmpty(t *testing.T) {
	home := clearHome(t)
	// Nothing exists under the fake home, so this exercises the default
	// location and the missing-file path at once.
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") should tolerate a missing default file: %v", err)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty for a missing file", cfg.Path)
	}
	roots, err := cfg.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, DirName, CommandsDirName); roots[0] != want {
		t.Errorf("roots = %v, want %q first", roots, want)
	}
}

func TestLoadReadsTheDefaultLocation(t *testing.T) {
	home := clearHome(t)
	dir := filepath.Join(home, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"fallback": "from-default-location"}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fallback != "from-default-location" {
		t.Fatalf("Fallback = %q", cfg.Fallback)
	}
	if !strings.HasSuffix(cfg.Path, FileName) {
		t.Errorf("Path = %q, want it to end in %s", cfg.Path, FileName)
	}
}

func TestExpandPath(t *testing.T) {
	home := clearHome(t)

	cases := map[string]string{
		"~":              home,
		"~/x":            filepath.Join(home, "x"),
		"~/a/b/../c":     filepath.Join(home, "a", "c"),
		"/abs/path":      "/abs/path",
		"/abs/./path":    "/abs/path",
		"relative":       "relative",
		"relative/../up": "up",
	}
	for in, want := range cases {
		got, err := ExpandPath(in)
		if err != nil {
			t.Errorf("ExpandPath(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ExpandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandPathOnlyExpandsLeadingTilde(t *testing.T) {
	clearHome(t)
	got, err := ExpandPath("/a/~/b")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/a/~/b" {
		t.Fatalf("ExpandPath = %q, want the inner tilde left alone", got)
	}
}
