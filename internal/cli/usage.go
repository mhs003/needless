package cli

const usageText = `n - run a command described in natural language

Usage:
  n <prompt>               run the command the prompt describes
  n --list-commands        list the available commands
  n --new-command [id]     create a new command
  n --edit-command [id]    open a command for editing
  n --help                 show this help

Options:
  -h, --help               show this help
  -l, --ls, --list-commands
                           list the available commands
  -n, --new, --new-command [id]
                           create a new command
  -e, --edit, --edit-command [id]
                           open a command for editing

The prompt may be quoted or given as separate words; both forms are treated as
one prompt:

  n run the project
  n "make me some cookies"

Commands are .nsc files under ~/.needless/commands, discovered recursively. The
file's path below that directory is the command's id, without the extension:
commands/git/status.nsc is the command "git/status".

Configuration is read from ~/.needless/config.json:

  {
    "command_roots": ["~/.needless/commands"],
    "fallback": "default",
    "weights": "~/.needless/models/needle3.cact",
    "system": "date: 2026-07-21 Tue 14:30; locale: en-US"
  }

Exit codes:
  0  success
  1  the command failed, or there was nothing to run
  2  the invocation was wrong
`
