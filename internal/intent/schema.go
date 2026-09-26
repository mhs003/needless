// Package intent turns commands into a tool schema for the model, and turns
// the model's reply back into a command plus argument values.
//
// This is the only place where natural language meets the command set, and it
// is deliberately narrow: the model picks one declared command by name and
// fills argument slots that the script already declared. Nothing the model
// returns is ever executed, and nothing outside the declared schema is
// accepted (AGENTS.md §2).
package intent

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/nscript"
)

// ToolsJSON builds the tool schema array that the engine consumes.
//
// The shape is the flat form Needle documents ("the raw JSON schema, what the
// engine actually consumes"). The command ID is used verbatim as the tool
// name; the engine accepts a '/' in a tool name and returns it unchanged, so
// no sanitising is needed and a returned name maps straight back to a command
// (D27).
func ToolsJSON(cmds []commands.Command) (string, error) {
	sorted := make([]commands.Command, len(cmds))
	copy(sorted, cmds)
	// The model's tool list must not shuffle between runs (D21). The registry
	// already sorts; sorting again makes this package independent of that.
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	schemas := make([]tool, 0, len(sorted))
	for _, cmd := range sorted {
		t, err := toolFor(cmd)
		if err != nil {
			return "", err
		}
		schemas = append(schemas, t)
	}

	raw, err := json.Marshal(schemas)
	if err != nil {
		return "", fmt.Errorf("intent: encode tool schemas: %w", err)
	}
	return string(raw), nil
}

// tool is one entry in the engine's tool array.
type tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  parameters `json:"parameters"`
}

type parameters struct {
	Type       string              `json:"type"`
	Properties map[string]property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// property is one argument schema. It marshals its own JSON so that a default
// of `false` or `0` is still emitted: a plain struct tag with omitempty would
// silently drop those, which are exactly the defaults most worth stating.
type property struct {
	Type    string
	Default any
	HasDef  bool
}

func (p property) MarshalJSON() ([]byte, error) {
	out := map[string]any{"type": p.Type}
	if p.HasDef {
		out["default"] = p.Default
	}
	return json.Marshal(out)
}

func toolFor(cmd commands.Command) (tool, error) {
	props := make(map[string]property, len(cmd.Args()))
	var required []string

	for _, arg := range cmd.Args() {
		p := property{Type: jsonType(arg.Type)}
		if arg.Default != nil {
			v, ok := literalValue(arg.Default)
			if !ok {
				return tool{}, fmt.Errorf("intent: command %q: default for %q is not a literal", cmd.ID, arg.Name)
			}
			p.Default, p.HasDef = v, true
		} else {
			required = append(required, arg.Name)
		}
		props[arg.Name] = p
	}

	return tool{
		Name:        cmd.ID,
		Description: cmd.Instruction(),
		Parameters: parameters{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
	}, nil
}

func jsonType(t nscript.Type) string {
	switch t {
	case nscript.TypeString:
		return "string"
	case nscript.TypeInt:
		return "integer"
	case nscript.TypeFloat:
		return "number"
	case nscript.TypeBool:
		return "boolean"
	}
	return "string"
}

// literalValue extracts the Go value of a literal expression. The parser has
// already guaranteed a default is a literal of the declared type (D15).
func literalValue(e nscript.Expr) (any, bool) {
	switch v := e.(type) {
	case *nscript.StringLit:
		return v.Value, true
	case *nscript.IntLit:
		return v.Value, true
	case *nscript.FloatLit:
		return v.Value, true
	case *nscript.BoolLit:
		return v.Value, true
	}
	return nil, false
}
