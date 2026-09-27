package worker

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// childEnv reads a live process's environment.
//
// /proc is Linux-only, which is the only platform v1 ships an engine for. A
// missing /proc (or a child that has already gone) skips rather than fails:
// this is a stronger check, not the only one.
func childEnv(t *testing.T, pid int) []string {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		t.Skipf("cannot read the child's environment: %v", err)
	}
	return strings.Split(string(raw), "\x00")
}

// TestStartedChildReallyHasTelemetryDisabled closes the gap between the policy
// and reality. workerEnv is pure and tested directly above, but that only
// matters if Start actually applies it to the process it spawns. This reads the
// live child's environment rather than trusting that the assignment happened.
func TestStartedChildReallyHasTelemetryDisabled(t *testing.T) {
	w := startStub(t, 7, "[]", 4096)

	env := childEnv(t, w.PID())
	if value, n := lookup(env, "NEEDLE_TELEMETRY"); n != 1 || value != "0" {
		t.Errorf("child NEEDLE_TELEMETRY = %q (%d entries), want %q once", value, n, "0")
	}
	if value, n := lookup(env, "DO_NOT_TRACK"); n != 1 || value != "1" {
		t.Errorf("child DO_NOT_TRACK = %q (%d entries), want %q once", value, n, "1")
	}
	// The parent's environment must still be inherited: the point is to add the
	// opt-out, not to hand the child a bare environment.
	if !envHas(env, "PATH") {
		t.Error("the child did not inherit PATH, so the environment was replaced rather than extended")
	}
}

// TestStartedChildKeepsAnExplicitOptIn is the other half: a user who exports
// NEEDLE_TELEMETRY=1 must reach the child, not be overridden by our default.
func TestStartedChildKeepsAnExplicitOptIn(t *testing.T) {
	t.Setenv("NEEDLE_TELEMETRY", "1")

	w := startStub(t, 7, "[]", 4096)

	env := childEnv(t, w.PID())
	if value, n := lookup(env, "NEEDLE_TELEMETRY"); n != 1 || value != "1" {
		t.Errorf("child NEEDLE_TELEMETRY = %q (%d entries), want %q once", value, n, "1")
	}
	if !envHas(env, "DO_NOT_TRACK") {
		t.Error("DO_NOT_TRACK should still be set when only NEEDLE_TELEMETRY was chosen")
	}
}

// TestWorkerEnvDoesNotAliasTheInput guards against the caller's environment
// slice being written through.
func TestWorkerEnvDoesNotAliasTheInput(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	got := workerEnv(base)

	if len(got) <= len(base) {
		t.Fatalf("expected the telemetry variables to be appended, got %v", got)
	}
	// Appending must not have happened in the caller's backing array.
	if len(base) != 1 || base[0] != "PATH=/usr/bin" {
		t.Fatalf("the input slice was modified: %v", base)
	}
}

func TestWorkerEnvDisablesTelemetry(t *testing.T) {
	cases := []struct {
		name string
		base []string
		// want is the value each key must end up with.
		want map[string]string
	}{
		{
			name: "nothing set: both are supplied",
			base: []string{"PATH=/usr/bin", "HOME=/home/x"},
			want: map[string]string{"NEEDLE_TELEMETRY": "0", "DO_NOT_TRACK": "1"},
		},
		{
			name: "an explicit opt-in survives",
			base: []string{"NEEDLE_TELEMETRY=1"},
			want: map[string]string{"NEEDLE_TELEMETRY": "1", "DO_NOT_TRACK": "1"},
		},
		{
			name: "an explicit DO_NOT_TRACK value survives",
			base: []string{"DO_NOT_TRACK=0"},
			want: map[string]string{"DO_NOT_TRACK": "0", "NEEDLE_TELEMETRY": "0"},
		},
		{
			name: "an empty value counts as a choice, not an omission",
			base: []string{"NEEDLE_TELEMETRY="},
			want: map[string]string{"NEEDLE_TELEMETRY": "", "DO_NOT_TRACK": "1"},
		},
		{
			name: "both already set",
			base: []string{"NEEDLE_TELEMETRY=1", "DO_NOT_TRACK=0"},
			want: map[string]string{"NEEDLE_TELEMETRY": "1", "DO_NOT_TRACK": "0"},
		},
		{
			name: "an unrelated variable that merely starts the same way",
			base: []string{"NEEDLE_TELEMETRY_EXTRA=1"},
			want: map[string]string{"NEEDLE_TELEMETRY": "0", "DO_NOT_TRACK": "1"},
		},
		{
			name: "empty environment",
			base: nil,
			want: map[string]string{"NEEDLE_TELEMETRY": "0", "DO_NOT_TRACK": "1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workerEnv(tc.base)

			// The base must be preserved verbatim: this is the child's whole
			// environment, not just the telemetry part of it.
			for _, kv := range tc.base {
				if !contains(got, kv) {
					t.Errorf("workerEnv dropped %q from the base", kv)
				}
			}
			for key, want := range tc.want {
				value, count := lookup(got, key)
				if count == 0 {
					t.Errorf("%s is not set at all", key)
					continue
				}
				if count > 1 {
					t.Errorf("%s appears %d times, want 1", key, count)
				}
				if value != want {
					t.Errorf("%s = %q, want %q", key, value, want)
				}
			}

			// Nothing may be duplicated, or the child would see an ambiguous
			// environment.
			seen := map[string]int{}
			for _, kv := range got {
				name, _, _ := strings.Cut(kv, "=")
				seen[name]++
			}
			for name, n := range seen {
				if n > 1 {
					t.Errorf("%s appears %d times in the result", name, n)
				}
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// lookup returns the value for key and how many times key appears.
func lookup(env []string, key string) (string, int) {
	prefix := key + "="
	value, count := "", 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value, count = strings.TrimPrefix(kv, prefix), count+1
		}
	}
	return value, count
}
