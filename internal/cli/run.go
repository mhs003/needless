package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/config"
	"github.com/mhs003/needless/internal/intent"
	"github.com/mhs003/needless/internal/runtime"
	"github.com/mhs003/needless/needle"
)

// selection is the outcome of resolving a prompt.
type selection struct {
	ok      bool
	command commands.Command
	args    map[string]any
}

// prompt resolves a natural-language prompt to a command and runs it.
func (a *app) prompt(ctx context.Context, prompt string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Fprintln(a.stderr, "n: no prompt given")
		fmt.Fprintln(a.stderr, "Try 'n --help'.")
		return ExitUsage
	}

	cfg, reg, roots, err := a.load()
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return ExitFailure
	}
	a.reportLoadErrors(reg)

	if reg.Len() == 0 {
		fmt.Fprintf(a.stderr, "n: no commands found in %s\n", strings.Join(roots, ", "))
		fmt.Fprintf(a.stderr, "Create one with 'n --new-command'.\n")
		return ExitFailure
	}

	sel := a.resolve(ctx, cfg, reg, prompt)
	if !sel.ok {
		return ExitFailure
	}
	return a.execute(ctx, sel.command, sel.args)
}

// resolve asks the model which command the prompt describes, falling back to
// the configured command when nothing matches.
func (a *app) resolve(ctx context.Context, cfg config.Config, reg *commands.Registry, prompt string) selection {
	cmds := reg.Commands()

	tools, err := intent.ToolsJSON(cmds)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return selection{}
	}
	weights, err := resolveWeights(cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return selection{}
	}

	client, closer, err := a.newModel(ctx, needle.Config{
		EnginePath:   cfg.Engine,
		WeightsPath:  weights,
		ToolsJSON:    tools,
		SystemPrompt: systemFact(cfg.System),
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return selection{}
	}
	defer closer.Close()

	res, err := intent.New(client, cmds).Match(ctx, prompt)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return selection{}
	}
	if res.Matched {
		return selection{ok: true, command: res.Command, args: res.Args}
	}

	a.reportNoMatch(res)
	return a.fallback(cfg, reg)
}

func (a *app) reportNoMatch(res intent.Result) {
	// The command was recognised but a required argument had no value. Saying
	// only "no command matched" would be untrue and would hide the one thing
	// the user can act on: which value the prompt failed to supply.
	if len(res.Unfilled) > 0 {
		fmt.Fprintf(a.stderr, "n: %s needs a value for %s, and the prompt does not provide one\n",
			res.Command.ID, strings.Join(res.Unfilled, ", "))
		return
	}
	if len(res.Suppressed) > 0 {
		// The engine withheld a call it was not confident about. Showing it
		// lets the user see what it nearly did.
		fmt.Fprintf(a.stderr, "n: no command matched (a low-confidence guess was withheld: %s)\n",
			res.Suppressed[0].Name)
		return
	}
	fmt.Fprintln(a.stderr, "n: no command matched the prompt")
	if res.Reasoning != "" {
		fmt.Fprintf(a.stderr, "n: %s\n", res.Reasoning)
	}
}

// fallback runs the configured fallback command, if there is one.
//
// The fallback is an ordinary .nsc command chosen by configuration (CLI spec
// §5). No model turn selected it, so it runs with the defaults declared in its
// own args block; a fallback that needs a value must declare a default for it
// (D42).
func (a *app) fallback(cfg config.Config, reg *commands.Registry) selection {
	if cfg.Fallback == "" {
		fmt.Fprintln(a.stderr, "n: no fallback is configured; nothing to run")
		return selection{}
	}
	fb, ok := reg.Lookup(cfg.Fallback)
	if !ok {
		fmt.Fprintf(a.stderr, "n: the configured fallback %q is not a known command\n", cfg.Fallback)
		return selection{}
	}
	args, err := intent.DefaultArgs(fb)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		return selection{}
	}
	fmt.Fprintf(a.stderr, "n: falling back to %q\n", fb.ID)
	return selection{ok: true, command: fb, args: args}
}

// execute performs the confirmation policy and runs the command.
func (a *app) execute(ctx context.Context, cmd commands.Command, args map[string]any) int {
	if runtime.RequiresConfirmation(cmd.Program) {
		fmt.Fprintf(a.stderr, "Command: %s\n", cmd.ID)
		fmt.Fprintf(a.stderr, "Action: %s\n", summary(cmd.Instruction()))
		fmt.Fprintln(a.stderr, "Confirmation required.")
		if !a.confirm() {
			fmt.Fprintln(a.stderr, "n: aborted")
			return ExitFailure
		}
	}

	ex := &runtime.Executor{Out: a.stdout, Err: a.stderr}
	if err := ex.Run(ctx, cmd.Program, args); err != nil {
		fmt.Fprintf(a.stderr, "n: %s: %v\n", cmd.ID, err)
		return ExitFailure
	}
	return ExitOK
}

// confirm asks the user to approve a command.
//
// The command's arguments are deliberately not echoed: a script may declare
// values like passwords that should not be printed (NSCRIPT spec §14). Only
// the command id and the first line of its instruction are shown.
func (a *app) confirm() bool {
	fmt.Fprint(a.stderr, "Continue? [y/N] ")
	line, err := a.readLine()
	if err != nil && strings.TrimSpace(line) == "" {
		// No answer, or no terminal to answer from. Declining is the safe
		// reading of an empty reply.
		fmt.Fprintln(a.stderr)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// readLine reads one line from stdin, buffering so that repeated prompts do
// not lose the remainder of a piped input.
func (a *app) readLine() (string, error) {
	if a.in == nil {
		a.in = bufio.NewReader(a.stdin)
	}
	return a.in.ReadString('\n')
}

// resolveWeights finds the model archive.
func resolveWeights(cfg config.Config) (string, error) {
	if cfg.Weights != "" {
		return cfg.Weights, nil
	}
	if p, ok := config.DefaultWeightsPath(); ok {
		return p, nil
	}
	return "", errors.New(`no model archive found; set "weights" in ~/.needless/config.json, ` +
		`or put needle3.cact in a models/ directory`)
}

// systemFact supplies the environment facts handed to the model.
//
// When the configuration names none, a date fact is generated, mirroring the
// reference Python binding (D44). A prompt like "remind me tomorrow" needs a
// reference date, and the model has no other way to know it.
func systemFact(configured string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return s
	}
	return "date: " + time.Now().Format("2006-01-02 Mon 15:04")
}

// summary returns the first non-empty line of a multi-line instruction, for
// one-line listings and confirmations.
func summary(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
