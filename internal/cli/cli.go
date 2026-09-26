// Package cli is the `n` command-line interface: flag parsing, user
// interaction, and exit codes.
//
// Exit codes are part of the public interface and are not invented here
// (CLI spec §13, AGENTS.md). Everything the user sees on stdout comes from the
// command they ran; Needless' own messages go to stderr, so that piping a
// command's output stays clean.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mhs003/needless/internal/commands"
	"github.com/mhs003/needless/internal/config"
	"github.com/mhs003/needless/internal/intent"
	"github.com/mhs003/needless/needle"
)

// Exit codes. These are the whole table; do not add meanings to them.
const (
	ExitOK      = 0 // success
	ExitFailure = 1 // the command failed, or nothing was run
	ExitUsage   = 2 // the invocation itself was wrong
)

// mode is which of the CLI's jobs was asked for.
type mode int

const (
	modePrompt mode = iota
	modeHelp
	modeList
	modeNew
	modeEdit
)

// options is the result of parsing the command line.
type options struct {
	mode    mode
	operand string // the identifier given to --new-command or --edit-command
	prompt  string // the natural-language prompt, words joined by spaces
}

// usageError is a bad invocation, reported with ExitUsage.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// modelFactory builds the model client. It is a seam so that the whole CLI can
// be tested without loading a 34 MiB model; production uses newModel.
type modelFactory func(ctx context.Context, cfg needle.Config) (intent.Completer, io.Closer, error)

func newModel(ctx context.Context, cfg needle.Config) (intent.Completer, io.Closer, error) {
	n, err := needle.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return n, n, nil
}

// app carries the streams every command needs.
type app struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	in         *bufio.Reader // lazy buffer over stdin, so prompts do not lose piped input
	configFile string
	newModel   modelFactory
}

// Main runs the CLI and returns the process exit code.
//
// It never panics on bad input and never calls os.Exit, so every path is
// testable.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, newModel: newModel}
	return a.main(ctx, args)
}

func (a *app) main(ctx context.Context, args []string) int {
	opt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.stderr, "n: %v\n", err)
		fmt.Fprintln(a.stderr, "Try 'n --help'.")
		return ExitUsage
	}

	switch opt.mode {
	case modeHelp:
		fmt.Fprint(a.stdout, usageText)
		return ExitOK
	case modeList, modeNew, modeEdit:
		return a.manage(opt)
	case modePrompt:
		return a.prompt(ctx, opt.prompt)
	}
	return ExitUsage
}

// parseArgs splits leading options from the prompt.
//
// Everything up to the first non-option word is options; the rest is the
// prompt, joined by spaces. That is what lets `n make me some cookies` and
// `n "make me some cookies"` mean the same thing (CLI spec §1).
func parseArgs(args []string) (options, error) {
	var opt options

	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}

		switch arg {
		case "-h", "--help":
			opt.mode = modeHelp
			i++
		case "-l", "--ls", "--list-commands":
			opt.mode = modeList
			i++
		case "-n", "--new", "--new-command":
			opt.mode = modeNew
			i++
			opt.operand, i = takeOperand(args, i)
		case "-e", "--edit", "--edit-command":
			opt.mode = modeEdit
			i++
			opt.operand, i = takeOperand(args, i)
		default:
			return opt, &usageError{msg: fmt.Sprintf("unknown option %q", arg)}
		}
	}

	if opt.mode != modePrompt && i < len(args) {
		return opt, &usageError{msg: fmt.Sprintf("unexpected argument %q after %s",
			args[i], modeName(opt.mode))}
	}

	opt.prompt = strings.Join(args[i:], " ")
	return opt, nil
}

// takeOperand consumes an optional non-option word.
func takeOperand(args []string, i int) (string, int) {
	if i < len(args) && (!strings.HasPrefix(args[i], "-") || args[i] == "-") {
		return args[i], i + 1
	}
	return "", i
}

func modeName(m mode) string {
	switch m {
	case modeHelp:
		return "--help"
	case modeList:
		return "--list-commands"
	case modeNew:
		return "--new-command"
	case modeEdit:
		return "--edit-command"
	}
	return ""
}

// load reads the configuration and discovers commands.
func (a *app) load() (config.Config, *commands.Registry, []string, error) {
	cfg, err := config.Load(a.configFile)
	if err != nil {
		return cfg, nil, nil, err
	}
	roots, err := cfg.Roots()
	if err != nil {
		return cfg, nil, nil, err
	}
	return cfg, commands.Discover(roots...), roots, nil
}

// reportLoadErrors warns about commands that failed to parse, without hiding
// the ones that loaded (D17).
func (a *app) reportLoadErrors(reg *commands.Registry) {
	for _, e := range reg.Errors() {
		fmt.Fprintf(a.stderr, "n: skipping %s: %v\n", e.Path, e.Err)
	}
}
