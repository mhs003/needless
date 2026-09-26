package nscript

import "fmt"

// builtins maps each builtin callable to its arity. All v1 builtins take
// exactly one argument.
var builtins = map[string]int{
	"print":  1,
	"error":  1,
	"string": 1,
	"int":    1,
	"float":  1,
	"bool":   1,
}

// isKeyword reports whether k is any reserved word. The keyword constants are
// declared contiguously between KwInstruction and KwBool, and
// TestKeywordsAreContiguous pins that.
func isKeyword(k Kind) bool { return k >= KwInstruction && k <= KwBool }

// checkCalls rejects calls to names that are neither builtins nor declared
// `fn`s, and rejects wrong argument counts. Catching this at parse time turns
// a typo into a message at the point of the mistake rather than a surprise
// halfway through a run.
func checkCalls(prog *Program) error {
	fns := map[string]*FnStmt{}
	if err := collectFns(prog.Run, fns); err != nil {
		return err
	}
	return checkBlock(prog.Run, fns)
}

func collectFns(b *Block, fns map[string]*FnStmt) error {
	if b == nil {
		return nil
	}
	for _, s := range b.Stmts {
		switch v := s.(type) {
		case *FnStmt:
			if prev, dup := fns[v.Name]; dup {
				return errorf(v.Pos, "duplicate function %q (first declared at %s)", v.Name, prev.Pos)
			}
			fns[v.Name] = v
			if err := collectFns(v.Body, fns); err != nil {
				return err
			}
		case *IfStmt:
			if err := collectFns(v.Then, fns); err != nil {
				return err
			}
			if err := collectFns(v.Else, fns); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkBlock(b *Block, fns map[string]*FnStmt) error {
	if b == nil {
		return nil
	}
	for _, s := range b.Stmts {
		if err := checkStmt(s, fns); err != nil {
			return err
		}
	}
	return nil
}

func checkStmt(s Stmt, fns map[string]*FnStmt) error {
	switch v := s.(type) {
	case *LetStmt:
		return checkExpr(v.Value, fns)
	case *IfStmt:
		if err := checkExpr(v.Cond, fns); err != nil {
			return err
		}
		if err := checkBlock(v.Then, fns); err != nil {
			return err
		}
		return checkBlock(v.Else, fns)
	case *FnStmt:
		return checkBlock(v.Body, fns)
	case *ExprStmt:
		return checkExpr(v.X, fns)
	case *EnvStmt:
		for _, ev := range v.Vars {
			if err := checkExpr(ev.Value, fns); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkExpr(e Expr, fns map[string]*FnStmt) error {
	switch v := e.(type) {
	case *CallExpr:
		if err := checkArity(v, fns); err != nil {
			return err
		}
		for _, a := range v.Args {
			if err := checkExpr(a, fns); err != nil {
				return err
			}
		}
	case *BinaryExpr:
		if err := checkExpr(v.Left, fns); err != nil {
			return err
		}
		return checkExpr(v.Right, fns)
	case *UnaryExpr:
		return checkExpr(v.X, fns)
	}
	return nil
}

func checkArity(call *CallExpr, fns map[string]*FnStmt) error {
	if want, ok := builtins[call.Fn]; ok {
		if len(call.Args) != want {
			return errorf(call.Pos, "%s takes exactly %s, got %d",
				call.Fn, plural(want, "argument", "arguments"), len(call.Args))
		}
		return nil
	}
	fn, ok := fns[call.Fn]
	if !ok {
		return errorf(call.Pos, "unknown function %q", call.Fn)
	}

	required := 0
	for _, p := range fn.Params {
		if p.Default == nil {
			required++
		}
	}
	got := len(call.Args)
	switch {
	case required == len(fn.Params):
		if got != required {
			return errorf(call.Pos, "%s takes exactly %s, got %d",
				call.Fn, plural(required, "argument", "arguments"), got)
		}
	case got < required || got > len(fn.Params):
		return errorf(call.Pos, "%s takes between %d and %d arguments, got %d",
			call.Fn, required, len(fn.Params), got)
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
