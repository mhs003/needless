// Package runtime executes a command's run block.
//
// It is the only part of Needless that runs anything. What it runs is decided
// entirely by the .nsc file: the model's contribution — which command, and
// what values — arrives as an ordinary variable environment and never becomes
// code (AGENTS.md §2).
package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"

	"github.com/mhs003/needless/internal/nscript"
)

// Executor runs run blocks.
type Executor struct {
	// Out receives print() output. It is the user's stdout, never diagnostics.
	// Nil discards.
	Out io.Writer

	// Err receives a child's stderr, streamed live rather than buffered, so
	// diagnostics from an embedded script appear as they happen. Nil discards
	// them; the CLI passes os.Stderr.
	Err io.Writer

	// Env is the base environment for exec blocks. Nil means os.Environ().
	Env []string

	// Dir is the working directory for exec blocks. Empty inherits the
	// current process's directory.
	Dir string
}

// RequiresConfirmation reports the command's confirmation policy, applying the
// language default when the script declares none.
//
// The default is false: the spec says commands that need confirmation declare
// it, so the policy is opt-in (D12). The CLI performs the interaction; the
// script only declares the intent (CLI spec §8).
func RequiresConfirmation(prog *nscript.Program) bool {
	if prog == nil || prog.Confirm == nil {
		return false
	}
	return *prog.Confirm
}

// runState carries the mutable state of one Run.
type runState struct {
	ctx   context.Context
	exec  *Executor
	out   io.Writer
	errw  io.Writer
	fns   map[string]*nscript.FnStmt
	root  *scope
	env   map[string]string
	depth int
}

// Run executes the program's run block with the given argument values.
//
// args must contain every declared argument; the matcher guarantees this and
// applies defaults, so a missing one here is a programming error rather than a
// user one, and is reported as such.
func (e *Executor) Run(ctx context.Context, prog *nscript.Program, args map[string]any) error {
	if prog == nil || prog.Run == nil {
		return errors.New("runtime: program has no run block")
	}

	out := e.Out
	if out == nil {
		out = io.Discard
	}

	st := &runState{
		ctx:  ctx,
		exec: e,
		out:  out,
		errw: e.Err,
		fns:  map[string]*nscript.FnStmt{},
		env:  map[string]string{},
	}
	collectFns(prog.Run, st.fns)

	st.root = newScope(nil)
	for _, arg := range prog.Args {
		value, ok := args[arg.Name]
		if !ok {
			return fmt.Errorf("runtime: command %q: no value for argument %q", prog.Instruction, arg.Name)
		}
		st.root.set(arg.Name, value)
	}

	err := st.runBlock(prog.Run, st.root)
	if errors.Is(err, errReturn) {
		return nil
	}
	return err
}

// collectFns gathers every function declared in the run block. Functions may be
// declared after their first use, so they are hoisted before execution.
func collectFns(b *nscript.Block, into map[string]*nscript.FnStmt) {
	for _, s := range b.Stmts {
		switch v := s.(type) {
		case *nscript.FnStmt:
			into[v.Name] = v
			collectFns(v.Body, into)
		case *nscript.IfStmt:
			collectFns(v.Then, into)
			if v.Else != nil {
				collectFns(v.Else, into)
			}
		}
	}
}

func (r *runState) runBlock(b *nscript.Block, sc *scope) error {
	for _, stmt := range b.Stmts {
		if err := r.execStmt(stmt, sc); err != nil {
			return err
		}
	}
	return nil
}

func (r *runState) execStmt(stmt nscript.Stmt, sc *scope) error {
	switch v := stmt.(type) {
	case *nscript.FnStmt:
		// A declaration, hoisted by collectFns; nothing to do at run time.
		return nil

	case *nscript.LetStmt:
		value, err := r.eval(v.Value, sc)
		if err != nil {
			return err
		}
		sc.set(v.Name, value)
		return nil

	case *nscript.ReturnStmt:
		return errReturn

	case *nscript.ExprStmt:
		_, err := r.eval(v.X, sc)
		return err

	case *nscript.IfStmt:
		cond, err := r.eval(v.Cond, sc)
		if err != nil {
			return err
		}
		b, ok := cond.(bool)
		if !ok {
			return errorf(v.Pos, "if needs a bool, got %s", typeName(cond))
		}
		if b {
			return r.runBlock(v.Then, sc)
		}
		if v.Else != nil {
			return r.runBlock(v.Else, sc)
		}
		return nil

	case *nscript.EnvStmt:
		for _, ev := range v.Vars {
			value, err := r.eval(ev.Value, sc)
			if err != nil {
				return err
			}
			s, ok := asString(value)
			if !ok {
				return errorf(ev.Pos, "env %s needs a string, int, float or bool, got %s",
					ev.Name, typeName(value))
			}
			r.env[ev.Name] = s
		}
		return nil

	case *nscript.ExecStmt:
		return r.execExternal(v)
	}
	return errorf(stmt.Position(), "cannot execute %T", stmt)
}

// execExternal runs an embedded script.
//
// The body goes to the interpreter on stdin, byte for byte, and the env block's
// values are passed through the environment. Nothing is interpolated into the
// body: the spec's rule is to avoid textual substitution into shell source, and
// handing the source over unmodified is the strongest form of that (D2, D35).
func (r *runState) execExternal(st *nscript.ExecStmt) error {
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// CommandContext so that cancelling the caller's context actually stops
	// the child; without it a long-running embedded script would outlive the
	// request that started it.
	cmd := exec.CommandContext(ctx, st.Interpreter)
	cmd.Stdin = bytes.NewReader(st.Body)
	cmd.Stdout = r.out
	// A child's stderr goes straight to the user, so progress and errors from
	// the embedded script appear as they happen rather than being buffered.
	cmd.Stderr = r.errw
	cmd.Env = append(r.baseEnv(), r.envList()...)
	cmd.Dir = r.exec.Dir

	err := cmd.Run()
	if err == nil {
		return nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{
			Pos:  st.Pos,
			Msg:  fmt.Sprintf("%s was cancelled: %v", st.Interpreter, ctxErr),
			Code: -1,
		}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &Error{
			Pos:  st.Pos,
			Msg:  fmt.Sprintf("%s exited with status %d", st.Interpreter, exitErr.ExitCode()),
			Code: exitErr.ExitCode(),
		}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errorf(st.Pos, "interpreter %q not found in PATH", st.Interpreter)
	}
	return errorf(st.Pos, "cannot run %q: %v", st.Interpreter, err)
}

func (r *runState) baseEnv() []string {
	if r.exec.Env != nil {
		return r.exec.Env
	}
	return os.Environ()
}

// envList renders the env block's bindings, sorted so the child's environment
// is reproducible.
func (r *runState) envList() []string {
	if len(r.env) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.env))
	for name := range r.env {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+r.env[name])
	}
	return out
}

// literalValue reads a literal expression's Go value.
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
