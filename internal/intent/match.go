package intent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/nscript"
	"github.com/mhs003/needless/needle"
)

// DefaultMaxTokens bounds the model's reply. A reply is one small JSON object
// naming a command and its arguments, so this is generous.
const DefaultMaxTokens = 256

// Completer is the part of needle.Needle that matching uses. Depending on the
// interface keeps matching testable without the 29 MiB model.
type Completer interface {
	CompleteResult(ctx context.Context, input string, maxNewTokens int) (needle.Result, error)
}

// Matcher resolves a prompt to one of the declared commands.
type Matcher struct {
	client       Completer
	byName       map[string]commands.Command
	maxNewTokens int
}

// New builds a matcher over cmds. The same command set must have been used to
// build the tool schema handed to the engine; one toolset per session.
func New(client Completer, cmds []commands.Command) *Matcher {
	byName := make(map[string]commands.Command, len(cmds))
	for _, c := range cmds {
		byName[c.ID] = c
	}
	return &Matcher{client: client, byName: byName, maxNewTokens: DefaultMaxTokens}
}

// SetMaxNewTokens overrides the reply budget. Zero or less restores the
// default.
func (m *Matcher) SetMaxNewTokens(n int) {
	if n <= 0 {
		n = DefaultMaxTokens
	}
	m.maxNewTokens = n
}

// Result is the outcome of one matching turn.
type Result struct {
	// Command is the selected command. Only meaningful when Matched is true.
	Command commands.Command

	// Matched reports whether a command was selected. False means the model
	// refused, which is a normal outcome for an off-topic prompt, not an
	// error (CLI spec §4, ARCHITECTURE.md D5).
	Matched bool

	// Args holds the resolved argument values, keyed by argument name.
	// Defaults are applied; every declared argument is present exactly once.
	Args map[string]any

	// Confidence is the model's calibrated score, or nil when the weights
	// carry no calibration head.
	Confidence *float64

	// Reasoning is the model's short derivation of the arguments.
	Reasoning string

	// Suppressed holds a call the engine withheld (low confidence or a
	// grounding gate). When it is non-empty, Matched is false.
	Suppressed []needle.FunctionCall

	// Reply is the raw decoded turn, for logging and diagnostics.
	Reply needle.Result
}

// ErrNoCommands is returned by Match when there is nothing to match against.
var ErrNoCommands = errors.New("intent: no commands are defined")

// Match runs one turn and interprets the reply.
func (m *Matcher) Match(ctx context.Context, prompt string) (Result, error) {
	if len(m.byName) == 0 {
		return Result{}, ErrNoCommands
	}

	reply, err := m.client.CompleteResult(ctx, prompt, m.maxNewTokens)
	if err != nil {
		return Result{}, fmt.Errorf("intent: complete: %w", err)
	}

	res := Result{
		Confidence: reply.Confidence,
		Reasoning:  reply.Reasoning,
		Suppressed: reply.SuppressedCalls,
		Reply:      reply,
	}

	if len(reply.FunctionCalls) == 0 {
		// A refusal, or a call the engine withheld. Either way there is
		// nothing to run.
		return res, nil
	}

	call := reply.FunctionCalls[0]
	cmd, ok := m.byName[call.Name]
	if !ok {
		// The grammar constrains the name to the declared set, so this means
		// the toolset and the matcher disagree, not that the user did
		// something wrong.
		return Result{}, fmt.Errorf("intent: model chose %q, which is not a declared command", call.Name)
	}

	args, err := resolveArgs(cmd, call.Arguments)
	if err != nil {
		return Result{}, err
	}

	res.Command = cmd
	res.Matched = true
	res.Args = args
	return res, nil
}

// resolveArgs validates the model's arguments against the command's
// declaration. Every declared argument ends up present: supplied values are
// coerced, and omitted ones take their default.
func resolveArgs(cmd commands.Command, raw json.RawMessage) (map[string]any, error) {
	supplied := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &supplied); err != nil {
			return nil, fmt.Errorf("intent: command %q: read arguments: %w", cmd.ID, err)
		}
	}

	declared := make(map[string]nscript.Arg, len(cmd.Args()))
	for _, a := range cmd.Args() {
		declared[a.Name] = a
	}

	// An argument the script never declared cannot be used, so accepting it
	// would silently drop part of what the model understood. The grammar
	// should make this impossible; if it happens, it is worth knowing (D28).
	if extra := undeclared(supplied, declared); len(extra) > 0 {
		return nil, fmt.Errorf("intent: command %q: model supplied undeclared argument(s) %s",
			cmd.ID, strings.Join(extra, ", "))
	}

	out := make(map[string]any, len(cmd.Args()))
	for _, arg := range cmd.Args() {
		value, ok := supplied[arg.Name]
		if !ok {
			if arg.Default == nil {
				return nil, fmt.Errorf("intent: command %q: missing required argument %q", cmd.ID, arg.Name)
			}
			def, ok := literalValue(arg.Default)
			if !ok {
				return nil, fmt.Errorf("intent: command %q: bad default for %q", cmd.ID, arg.Name)
			}
			out[arg.Name] = def
			continue
		}
		coerced, err := coerce(arg, value)
		if err != nil {
			return nil, fmt.Errorf("intent: command %q: %w", cmd.ID, err)
		}
		out[arg.Name] = coerced
	}
	return out, nil
}

func undeclared(supplied map[string]any, declared map[string]nscript.Arg) []string {
	var extra []string
	for name := range supplied {
		if _, ok := declared[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return extra
}

// coerce converts a JSON-decoded value to the argument's declared type.
//
// JSON gives strings, float64 and bool. A small model sometimes quotes a
// number or writes one for a string, so the conversions below are deliberately
// generous where the intent is unambiguous, and refuse where it is not: 1.5
// for an int is a mistake, not a rounding opportunity.
func coerce(arg nscript.Arg, raw any) (any, error) {
	switch arg.Type {
	case nscript.TypeString:
		switch v := raw.(type) {
		case string:
			return v, nil
		case float64:
			return formatNumber(v), nil
		case bool:
			return strconv.FormatBool(v), nil
		}

	case nscript.TypeInt:
		switch v := raw.(type) {
		case float64:
			if v != math.Trunc(v) || math.IsInf(v, 0) || math.IsNaN(v) {
				return nil, badValue(arg, raw, "an integer")
			}
			return int64(v), nil
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n, nil
			}
		}

	case nscript.TypeFloat:
		switch v := raw.(type) {
		case float64:
			return v, nil
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f, nil
			}
		}

	case nscript.TypeBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
	}
	return nil, badValue(arg, raw, "a value of type "+arg.Type.String())
}

func badValue(arg nscript.Arg, raw any, want string) error {
	return fmt.Errorf("argument %q: cannot use %s as %s", arg.Name, describe(raw), want)
}

func describe(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case nil:
		return "null"
	case map[string]any, []any:
		return "a composite value"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// formatNumber renders a JSON number without a trailing ".0", so a string
// argument that the model wrote as a number reads back the way a person would
// write it.
func formatNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
