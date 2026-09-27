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
	// Command is the selected command. It is meaningful when Matched is true,
	// and also when Unfilled is non-empty: the command that was recognised but
	// could not be filled.
	Command commands.Command

	// Matched reports whether a command was selected. False means the model
	// refused, which is a normal outcome for an off-topic prompt, not an
	// error (CLI spec §4, ARCHITECTURE.md D5).
	Matched bool

	// Args holds the resolved argument values, keyed by argument name.
	// Defaults are applied; every declared argument is present exactly once.
	Args map[string]any

	// Unfilled names the required arguments the model gave no value for, in
	// declaration order. When it is non-empty, Matched is false: the command
	// was recognised, but the prompt does not carry enough to run it.
	//
	// This is a refusal rather than an error (D50). The model did nothing
	// wrong and neither did the user, so returning an error would report a
	// perfectly ordinary outcome as a malfunction. The caller reports which
	// argument was missing and may fall back.
	Unfilled []string

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

	args, unfilled, err := resolveArgs(cmd, call.Arguments)
	if err != nil {
		return Result{}, err
	}
	if len(unfilled) > 0 {
		// The command was recognised, but a required argument has no value,
		// so there is nothing runnable. That is a refusal, not a failure
		// (D50, and the fix for B1).
		res.Command = cmd
		res.Unfilled = unfilled
		return res, nil
	}

	res.Command = cmd
	res.Matched = true
	res.Args = args
	return res, nil
}

// DefaultArgs resolves a command's arguments from its declared defaults alone,
// with no model turn.
//
// This is what a configured fallback runs with: nothing selected it, so there
// is no model output to read values from. A fallback therefore needs a default
// for every argument it declares, and the error below says so rather than
// running the script with a hole in it (D42).
func DefaultArgs(cmd commands.Command) (map[string]any, error) {
	out := make(map[string]any, len(cmd.Args()))
	var missing []string

	for _, arg := range cmd.Args() {
		if arg.Default == nil {
			missing = append(missing, arg.Name)
			continue
		}
		v, ok := literalValue(arg.Default)
		if !ok {
			return nil, fmt.Errorf("intent: command %q: bad default for %q", cmd.ID, arg.Name)
		}
		out[arg.Name] = v
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"intent: command %q cannot be used as a fallback: %s would need a value, and nothing selects a fallback",
			cmd.ID, strings.Join(missing, ", "))
	}
	return out, nil
}

// resolveArgs validates the model's arguments against the command's
// declaration. Every declared argument ends up present: supplied values are
// coerced, and omitted ones take their default.
//
// The returned names are required arguments the model left without a value.
// They make the command unrunnable, which the caller reports as a refusal
// rather than an error. The error return is reserved for a reply that is
// malformed or contradicts the declaration — a different kind of problem.
func resolveArgs(cmd commands.Command, raw json.RawMessage) (map[string]any, []string, error) {
	supplied := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &supplied); err != nil {
			return nil, nil, fmt.Errorf("intent: command %q: read arguments: %w", cmd.ID, err)
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
		return nil, nil, fmt.Errorf("intent: command %q: model supplied undeclared argument(s) %s",
			cmd.ID, strings.Join(extra, ", "))
	}

	out := make(map[string]any, len(cmd.Args()))
	var unfilled []string

	for _, arg := range cmd.Args() {
		// A slot the model filled with something meaningless is treated as
		// empty, whatever the argument's declared type (D51). It is checked
		// before coercion rather than after, because a boolean is not a value
		// a string can be made from without inventing one — reading it as an
		// error would make the rule uniform for strings and an exception for
		// everything else. This is about what a slot means, which is why it
		// lives here and not in coerce.
		if value, ok := supplied[arg.Name]; ok && !hasNoValue(arg, value) {
			coerced, err := coerce(arg, value)
			if err != nil {
				return nil, nil, fmt.Errorf("intent: command %q: %w", cmd.ID, err)
			}
			out[arg.Name] = coerced
			continue
		}

		// Either absent or meaningless: take the declared default if there is
		// one. An argument whose author wrote `x: string = ""` has opted into
		// an empty value and still gets one, because the default is what is
		// used.
		if arg.Default == nil {
			unfilled = append(unfilled, arg.Name)
			continue
		}
		def, ok := literalValue(arg.Default)
		if !ok {
			return nil, nil, fmt.Errorf("intent: command %q: bad default for %q", cmd.ID, arg.Name)
		}
		out[arg.Name] = def
	}
	return out, unfilled, nil
}

// hasNoValue reports whether what the model supplied means "nothing", as opposed
// to a value that happens to be wrong.
//
// A model with nothing to put in a slot still has to return a call, so it fills
// the slot with something meaningless. Three shapes mean that, and all three are
// read as absent rather than as a value (D51):
//
//   - the empty string, which is how B1 first surfaced;
//   - JSON null, which is literally "no value";
//   - a boolean in a slot that is not a boolean, which stringifies to "false" —
//     the worst possible outcome, because it looks like a value. That one
//     reached a shell script as `cd false` and failed on a git status prompt.
//
// A number for a string slot is deliberately *not* in this list: "3000" for a
// port is a real conversion the model may well have meant (D29). Only tokens
// that carry no information about what the user asked for count.
func hasNoValue(arg nscript.Arg, raw any) bool {
	switch v := raw.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return arg.Type != nscript.TypeBool
	}
	return false
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
//
// A boolean is never converted to a string: `false` becoming "false" is not a
// generous reading of anything, it is inventing a value, and resolveArgs filters
// that case out before reaching here.
func coerce(arg nscript.Arg, raw any) (any, error) {
	switch arg.Type {
	case nscript.TypeString:
		switch v := raw.(type) {
		case string:
			return v, nil
		case float64:
			return formatNumber(v), nil
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
