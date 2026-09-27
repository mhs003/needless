package intent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/testenv"
)

// The shipped examples exercise a handful of commands, which is the size the
// retrieval head renders in full. Nothing tested what happens when there are
// many, where the engine has to choose a subset.

// scaleCommands builds TopicCount real commands.
func scaleCommands(t *testing.T) []commands.Command {
	t.Helper()
	out := make([]commands.Command, 0, testenv.TopicCount)
	for i := 0; i < testenv.TopicCount; i++ {
		id, src := testenv.ScaleCommand(i)
		out = append(out, command(t, id, src))
	}
	return out
}

// TestScaleToolsJSONCoversEveryCommand pins the schema at size: no command may
// be dropped, duplicated or shuffled.
func TestScaleToolsJSONCoversEveryCommand(t *testing.T) {
	cmds := scaleCommands(t)

	got, err := ToolsJSON(cmds)
	if err != nil {
		t.Fatal(err)
	}

	var decoded []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Type       string   `json:"type"`
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("ToolsJSON produced invalid JSON at scale: %v", err)
	}
	if len(decoded) != testenv.TopicCount {
		t.Fatalf("tools = %d, want %d", len(decoded), testenv.TopicCount)
	}

	seen := map[string]bool{}
	var prev string
	for _, tool := range decoded {
		if seen[tool.Name] {
			t.Errorf("tool %q appears twice", tool.Name)
		}
		seen[tool.Name] = true

		// Sorted, or the tool list would shuffle between runs and matching
		// would become unstable (D21).
		if tool.Name < prev {
			t.Errorf("tools are not sorted: %q came after %q", tool.Name, prev)
		}
		prev = tool.Name

		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.Parameters.Type != "object" {
			t.Errorf("tool %q parameters.type = %q", tool.Name, tool.Parameters.Type)
		}
	}

	for _, cmd := range cmds {
		if !seen[cmd.ID] {
			t.Errorf("command %q is missing from the schema", cmd.ID)
		}
	}
}

// TestScaleMatchMapsEveryNameBack checks the other direction: whichever command
// the engine names, the matcher resolves it to the same one. With 60 commands
// in a map, a lookup that was subtly wrong would show up here rather than in
// production.
func TestScaleMatchMapsEveryNameBack(t *testing.T) {
	cmds := scaleCommands(t)

	for _, want := range cmds {
		t.Run(want.ID, func(t *testing.T) {
			model := &fakeModel{reply: call(want.ID, map[string]any{})}
			m := New(model, cmds)

			res, err := m.Match(context.Background(), "go")
			if err != nil {
				t.Fatal(err)
			}
			if !res.Matched {
				t.Fatalf("%s did not match", want.ID)
			}
			if res.Command.ID != want.ID {
				t.Fatalf("matched %q, want %q", res.Command.ID, want.ID)
			}
		})
	}
}

// TestScaleDefaultsAreIndependent guards a subtle aliasing bug: with many
// commands, a shared default value would leak between them.
func TestScaleDefaultsAreIndependent(t *testing.T) {
	cmd := command(t, "x", `instruction "x"
args {
    who: string
}
run { print("y") }
`)
	cmds := append(scaleCommands(t), cmd)

	model := &fakeModel{reply: call("x", map[string]any{"who": "me"})}
	res, err := New(model, cmds).Match(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Args, map[string]any{"who": "me"}) {
		t.Fatalf("args = %#v", res.Args)
	}
}
