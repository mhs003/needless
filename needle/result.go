package needle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Result is the decoded form of one turn's JSON reply.
//
// The engine always returns exactly one of these per Complete call. The
// important rule for callers: an off-topic or unsupported request comes back
// as an empty FunctionCalls list (a refusal) — there is no free-text fallback.
// Always handle the empty case.
type Result struct {
	// Type is "call" when the model wants tool calls, "respond" when the
	// loop is finished.
	Type string `json:"type"`
	// Success is false when the engine reports a handled failure.
	Success bool `json:"success"`
	// Error and ErrorCode describe a handled failure, if any.
	Error     string `json:"error"`
	ErrorCode string `json:"error_code"`
	// FunctionCalls holds the calls to execute. Empty means refusal.
	FunctionCalls []FunctionCall `json:"function_calls"`
	// SuppressedCalls holds a call the engine withheld (confidence below
	// 0.1 or a grounding gate fired); FunctionCalls is empty in that case.
	// Show it to the user to confirm, or treat the turn as a refusal.
	SuppressedCalls []FunctionCall `json:"suppressed_calls"`
	// Reasoning is a short unconstrained derivation of each argument.
	Reasoning string `json:"reasoning"`
	// Confidence is a calibrated score in [0,1]. It is nil when the weights
	// carry no calibration head (for example a locally fine-tuned archive).
	Confidence *float64 `json:"confidence"`
	// Throughput metrics reported by the engine.
	PrefillTPS float64 `json:"prefill_tps"`
	DecodeTPS  float64 `json:"decode_tps"`
	// Validation carries the engine's grounding report when present.
	Validation json.RawMessage `json:"validation,omitempty"`
}

// FunctionCall is one tool invocation. Arguments is the raw JSON object and is
// grammar-guaranteed to match the tool's declared schema.
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Bind unmarshals the call's arguments into v.
func (c FunctionCall) Bind(v any) error {
	if len(c.Arguments) == 0 {
		return errors.New("needle: call has no arguments")
	}
	return json.Unmarshal(c.Arguments, v)
}

// Refusal reports whether the turn was a refusal (no call to run).
func (r Result) Refusal() bool { return len(r.FunctionCalls) == 0 }

// ParseResult decodes the raw JSON reply returned by Complete.
func ParseResult(raw string) (Result, error) {
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Result{}, fmt.Errorf("needle: parse reply: %w", err)
	}
	return r, nil
}

// CompleteResult is Complete plus decoding. It is the convenience entry point
// most callers want.
func (n *Needle) CompleteResult(ctx context.Context, input string, maxNewTokens int) (Result, error) {
	raw, err := n.Complete(ctx, input, maxNewTokens)
	if err != nil {
		return Result{}, err
	}
	return ParseResult(raw)
}
