package runtime

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/mhs003/needless/internal/nscript"
)

// Error is a failure inside a script.
type Error struct {
	// Pos is where the failure happened, when it is traceable to source.
	Pos nscript.Pos

	// Msg describes the failure.
	Msg string

	// Code is the child process exit code when the failure came from an exec
	// block, and 0 otherwise.
	Code int
}

func (e *Error) Error() string {
	if e.Pos.Line > 0 {
		return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
	}
	return e.Msg
}

func errorf(pos nscript.Pos, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// errReturn unwinds a run or function body when it hits `return`. It is
// compared by identity, never treated as a failure by callers.
var errReturn = errors.New("runtime: return")

// maxCallDepth bounds recursion. There is no loop construct in v1, but a
// self-recursive `fn` would otherwise blow the Go stack.
const maxCallDepth = 64

// --- values --------------------------------------------------------------

// The runtime uses four value types: string, int64, float64 and bool. They map
// one-to-one onto the declared argument types, so a value coming from the
// matcher is already the right shape.

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case int64:
		return "int"
	case float64:
		return "float"
	case bool:
		return "bool"
	case nil:
		return "nothing"
	}
	return fmt.Sprintf("%T", v)
}

// asString renders a value the way the string() builtin does.
func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// mustString renders a value for print and env, where any primitive is
// meaningful.
func mustString(v any) string {
	s, ok := asString(v)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

func isNumber(v any) bool {
	switch v.(type) {
	case int64, float64:
		return true
	}
	return false
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// --- scope ---------------------------------------------------------------

// scope is a variable environment. A run block and each function call get one;
// `if` blocks share the enclosing scope, so a `let` inside one is still visible
// after it.
type scope struct {
	vars   map[string]any
	parent *scope
}

func newScope(parent *scope) *scope {
	return &scope{vars: map[string]any{}, parent: parent}
}

func (s *scope) get(name string) (any, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (s *scope) set(name string, v any) { s.vars[name] = v }

// --- expressions ---------------------------------------------------------

func (r *runState) eval(e nscript.Expr, sc *scope) (any, error) {
	switch v := e.(type) {
	case *nscript.StringLit:
		return v.Value, nil

	case *nscript.IntLit:
		return v.Value, nil

	case *nscript.FloatLit:
		return v.Value, nil

	case *nscript.BoolLit:
		return v.Value, nil

	case *nscript.IdentExpr:
		value, ok := sc.get(v.Name)
		if !ok {
			return nil, errorf(v.Pos, "%q is not defined", v.Name)
		}
		return value, nil

	case *nscript.UnaryExpr:
		return r.evalUnary(v, sc)

	case *nscript.BinaryExpr:
		return r.evalBinary(v, sc)

	case *nscript.CallExpr:
		return r.evalCall(v, sc)
	}
	return nil, errorf(e.Position(), "cannot evaluate %T", e)
}

func (r *runState) evalUnary(v *nscript.UnaryExpr, sc *scope) (any, error) {
	operand, err := r.eval(v.X, sc)
	if err != nil {
		return nil, err
	}
	switch v.Op {
	case nscript.Bang:
		b, ok := operand.(bool)
		if !ok {
			return nil, errorf(v.Pos, "\"!\" needs a bool, got %s", typeName(operand))
		}
		return !b, nil

	case nscript.Minus:
		switch t := operand.(type) {
		case int64:
			return -t, nil
		case float64:
			return -t, nil
		}
		return nil, errorf(v.Pos, "\"-\" needs a number, got %s", typeName(operand))
	}
	return nil, errorf(v.Pos, "cannot apply %s", v.Op)
}

func (r *runState) evalBinary(v *nscript.BinaryExpr, sc *scope) (any, error) {
	// Logical operators short-circuit, so the right side is only evaluated
	// when it matters.
	switch v.Op {
	case nscript.AndAnd, nscript.OrOr:
		return r.evalLogical(v, sc)
	}

	left, err := r.eval(v.Left, sc)
	if err != nil {
		return nil, err
	}
	right, err := r.eval(v.Right, sc)
	if err != nil {
		return nil, err
	}

	switch v.Op {
	case nscript.Plus:
		// `+` concatenates strings and adds numbers, but never mixes the two.
		// The spec's own example writes string(amount) rather than relying on
		// an implicit conversion, and silently stringifying would hide a
		// mistake that changes what a script means (D34).
		if ls, ok := left.(string); ok {
			rs, ok := right.(string)
			if !ok {
				return nil, errorf(v.Pos, "cannot add %s to string; use string(...)", typeName(right))
			}
			return ls + rs, nil
		}
		return arith(v, "+", left, right)

	case nscript.Minus, nscript.Star, nscript.Slash:
		return arith(v, v.Op.String(), left, right)

	case nscript.Eq, nscript.Ne:
		equal, err := valuesEqual(v.Pos, left, right)
		if err != nil {
			return nil, err
		}
		if v.Op == nscript.Ne {
			return !equal, nil
		}
		return equal, nil

	case nscript.Lt, nscript.Le, nscript.Gt, nscript.Ge:
		return compare(v, left, right)
	}
	return nil, errorf(v.Pos, "cannot apply %s", v.Op)
}

func (r *runState) evalLogical(v *nscript.BinaryExpr, sc *scope) (any, error) {
	left, err := r.eval(v.Left, sc)
	if err != nil {
		return nil, err
	}
	lb, ok := left.(bool)
	if !ok {
		return nil, errorf(v.Pos, "%s needs a bool, got %s", v.Op, typeName(left))
	}
	if v.Op == nscript.AndAnd && !lb {
		return false, nil
	}
	if v.Op == nscript.OrOr && lb {
		return true, nil
	}

	right, err := r.eval(v.Right, sc)
	if err != nil {
		return nil, err
	}
	rb, ok := right.(bool)
	if !ok {
		return nil, errorf(v.Pos, "%s needs a bool, got %s", v.Op, typeName(right))
	}
	return rb, nil
}

func arith(v *nscript.BinaryExpr, op string, left, right any) (any, error) {
	if !isNumber(left) || !isNumber(right) {
		return nil, errorf(v.Pos, "%s needs two numbers, got %s and %s", op, typeName(left), typeName(right))
	}
	// Two ints stay an int; anything involving a float widens.
	li, lok := left.(int64)
	ri, rok := right.(int64)
	if lok && rok {
		switch v.Op {
		case nscript.Plus:
			return li + ri, nil
		case nscript.Minus:
			return li - ri, nil
		case nscript.Star:
			return li * ri, nil
		case nscript.Slash:
			if ri == 0 {
				return nil, errorf(v.Pos, "division by zero")
			}
			return li / ri, nil
		}
	}

	lf, _ := asFloat(left)
	rf, _ := asFloat(right)
	switch v.Op {
	case nscript.Plus:
		return lf + rf, nil
	case nscript.Minus:
		return lf - rf, nil
	case nscript.Star:
		return lf * rf, nil
	case nscript.Slash:
		if rf == 0 {
			return nil, errorf(v.Pos, "division by zero")
		}
		return lf / rf, nil
	}
	return nil, errorf(v.Pos, "unknown operator %s", op)
}

func valuesEqual(pos nscript.Pos, left, right any) (bool, error) {
	// int and float compare numerically, so `1 == 1.0` is true.
	if isNumber(left) && isNumber(right) {
		lf, _ := asFloat(left)
		rf, _ := asFloat(right)
		return lf == rf, nil
	}
	switch l := left.(type) {
	case string:
		rs, ok := right.(string)
		if !ok {
			return false, errorf(pos, "cannot compare string with %s", typeName(right))
		}
		return l == rs, nil
	case bool:
		rb, ok := right.(bool)
		if !ok {
			return false, errorf(pos, "cannot compare bool with %s", typeName(right))
		}
		return l == rb, nil
	}
	return false, errorf(pos, "cannot compare %s with %s", typeName(left), typeName(right))
}

func compare(v *nscript.BinaryExpr, left, right any) (any, error) {
	op := v.Op.String()

	if isNumber(left) && isNumber(right) {
		lf, _ := asFloat(left)
		rf, _ := asFloat(right)
		switch v.Op {
		case nscript.Lt:
			return lf < rf, nil
		case nscript.Le:
			return lf <= rf, nil
		case nscript.Gt:
			return lf > rf, nil
		case nscript.Ge:
			return lf >= rf, nil
		}
	}

	if ls, ok := left.(string); ok {
		rs, ok := right.(string)
		if !ok {
			return nil, errorf(v.Pos, "%s needs two values of the same type, got string and %s", op, typeName(right))
		}
		switch v.Op {
		case nscript.Lt:
			return ls < rs, nil
		case nscript.Le:
			return ls <= rs, nil
		case nscript.Gt:
			return ls > rs, nil
		case nscript.Ge:
			return ls >= rs, nil
		}
	}

	return nil, errorf(v.Pos, "%s needs two numbers or two strings, got %s and %s",
		op, typeName(left), typeName(right))
}

// --- calls ---------------------------------------------------------------

func (r *runState) evalCall(c *nscript.CallExpr, sc *scope) (any, error) {
	// The parser has already rejected unknown names and wrong arity (D16), so
	// anything reaching here is a known callable.
	if fn, ok := r.fns[c.Fn]; ok {
		return r.callUser(c, fn, sc)
	}
	return r.callBuiltin(c, sc)
}

func (r *runState) callUser(c *nscript.CallExpr, fn *nscript.FnStmt, sc *scope) (any, error) {
	if r.depth >= maxCallDepth {
		return nil, errorf(c.Pos, "call depth exceeded %d; is %q recursive?", maxCallDepth, c.Fn)
	}

	// A function body gets its own scope with the run block as parent, so a
	// call cannot see the caller's locals and cannot leak its own.
	callScope := newScope(r.root)
	for i, param := range fn.Params {
		if i >= len(c.Args) {
			if param.Default == nil {
				return nil, errorf(c.Pos, "missing argument %q for %q", param.Name, c.Fn)
			}
			def, ok := literalValue(param.Default)
			if !ok {
				return nil, errorf(c.Pos, "bad default for %q", param.Name)
			}
			callScope.set(param.Name, def)
			continue
		}
		value, err := r.eval(c.Args[i], sc)
		if err != nil {
			return nil, err
		}
		coerced, err := convert(param.Type, value, c.Pos, param.Name)
		if err != nil {
			return nil, err
		}
		callScope.set(param.Name, coerced)
	}

	r.depth++
	err := r.runBlock(fn.Body, callScope)
	r.depth--
	if errors.Is(err, errReturn) {
		return nil, nil
	}
	return nil, err
}

func (r *runState) callBuiltin(c *nscript.CallExpr, sc *scope) (any, error) {
	value, err := r.eval(c.Args[0], sc)
	if err != nil {
		return nil, err
	}

	switch c.Fn {
	case "print":
		fmt.Fprintln(r.out, mustString(value))
		return nil, nil

	case "error":
		return nil, &Error{Pos: c.Pos, Msg: mustString(value)}

	case "string":
		s, ok := asString(value)
		if !ok {
			return nil, errorf(c.Pos, "string() needs a string, int, float or bool, got %s", typeName(value))
		}
		return s, nil

	case "int":
		return toInt(value, c.Pos)

	case "float":
		if f, ok := asFloat(value); ok {
			return f, nil
		}
		if s, ok := value.(string); ok {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, nil
			}
		}
		return nil, errorf(c.Pos, "float() needs a number or a numeric string, got %s", typeName(value))

	case "bool":
		if b, ok := value.(bool); ok {
			return b, nil
		}
		if s, ok := value.(string); ok {
			switch s {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
		return nil, errorf(c.Pos, "bool() needs a bool or \"true\"/\"false\", got %s", typeName(value))
	}
	return nil, errorf(c.Pos, "unknown function %q", c.Fn)
}

func toInt(value any, pos nscript.Pos) (any, error) {
	switch t := value.(type) {
	case int64:
		return t, nil
	case float64:
		if t != math.Trunc(t) {
			return nil, errorf(pos, "int() cannot take the fractional value %v", t)
		}
		return int64(t), nil
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil && f == math.Trunc(f) {
			return int64(f), nil
		}
	}
	return nil, errorf(pos, "int() needs a number or a numeric string, got %s", typeName(value))
}

// convert coerces a value to a declared parameter type. It is the same job the
// intent layer does for command arguments, applied to function calls.
func convert(t nscript.Type, value any, pos nscript.Pos, name string) (any, error) {
	switch t {
	case nscript.TypeString:
		if s, ok := asString(value); ok {
			return s, nil
		}
	case nscript.TypeInt:
		if v, err := toInt(value, pos); err == nil {
			return v, nil
		}
	case nscript.TypeFloat:
		if f, ok := asFloat(value); ok {
			return f, nil
		}
		if s, ok := value.(string); ok {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, nil
			}
		}
	case nscript.TypeBool:
		if b, ok := value.(bool); ok {
			return b, nil
		}
	}
	return nil, errorf(pos, "argument %q needs %s, got %s", name, t, typeName(value))
}
