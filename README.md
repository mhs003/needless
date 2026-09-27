# Needless

`n` maps a natural-language prompt to one of your own **nscript** commands and
runs it. The matching is done on-device by
[Needle 3](https://github.com/cactus-compute/needle) — no network, no API key.

```
$ n start the dev server for ./shop on port 3000
Starting ./shop on port 3000
It will run in the foreground until stopped.
detected a Node project (package.json)
```

The model never writes executable text. It picks **one** command from the ones
you have defined and fills in arguments that command already declared. The
`.nsc` file decides what actually runs.

---

## Quick start

```sh
make build              # builds ./n and ./needle-worker
make install-examples   # copies examples/commands into ~/.needless/commands
./n --list-commands
./n "show the git status of /path/to/repo"
```

`./n` and `./needle-worker` must sit in the same directory; `make build` puts
them both in the repository root. `make doctor` reports what it can find.

Requires a `models/needle3.cact` archive. By default it is looked for in
`<exe dir>/models/`, `./models/`, then `~/.needless/models/`. Set `weights` in
the config (below) to point somewhere else.

---

## Writing a command

A command is a `.nsc` file under `~/.needless/commands/`. Its path below that
directory is its id: `commands/git/status.nsc` is the command `git/status`.

```nsc
instruction """
Show the current Git status of a project.

Use this command when the user asks what has changed or what is modified.
"""

confirm false

args {
    project: string
    short: bool = true
}

run {
    env {
        PROJECT = project
        SHORT = short
    }

    exec bash <<(
set -e
cd "$PROJECT"
git status --short
)<<
}
```

- **`instruction`** is the only thing the model matches a prompt against. Write
  it in the words a user would use.
- **`args`** declares the slots the model may fill. A default makes an argument
  optional; `string`, `int`, `float` and `bool` are the types.

  An argument with **no** default must be given a value. If the prompt does not
  supply one, `n` says which one it could not find and refuses to run, rather
  than passing an empty value through and letting the script fail somewhere
  inside. An empty string always counts as "no value"; write `x: string = ""`
  if you genuinely want an empty one.
- **`confirm true`** makes `n` ask before running. It defaults to `false`.
- **`run`** is the implementation, built from `let`, `if`/`else`, `fn`,
  `return`, `print`, `error`, `env` and `exec`.

`exec <interpreter> <<( ... )<<` hands the block to another interpreter on
stdin, byte for byte — nscript never rewrites or interpolates it. Values reach
it through the environment, which is why the example above sets `env`.

`n --new-command` creates a file to start from, `n --edit-command` opens one,
`n --show-command` prints one, and `n --remove` deletes one. Each accepts a
command id and offers a numbered selection when you leave it out; `--remove`
asks before deleting and treats no answer as "no".

---

## Configuration

`~/.needless/config.json`, all keys optional:

```json
{
  "command_roots": ["~/.needless/commands"],
  "fallback": "default",
  "weights": "~/.needless/models/needle3.cact",
  "engine": "~/.needless/lib/libneedle.so",
  "system": "date: 2026-07-21 Tue 14:30; locale: en-US"
}
```

- **`command_roots`** are searched in order; the first to provide an id wins.
- **`fallback`** is the id of a command to run when nothing matches. Since no
  model turn selects it, it must give every argument it needs a default.
- **`system`** is environment facts for the model. A `date:` fact is supplied
  automatically when you do not set one.

Unknown keys are an error, not silently ignored — a typo should not do nothing.

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | the command failed, or there was nothing to run |
| `2` | the invocation was wrong |

An unmatched prompt and a declined confirmation are both `1`: nothing ran, so
reporting success would let `n cleanup && …` carry on as though it had.

stdout carries the command's own output and nothing else, so it is safe to
pipe. Needless' messages, and confirmation prompts, go to stderr.

---

## Learn more

- **[Writing commands](docs/writing-commands.md)** — how to word an
  `instruction` so matching works, arguments and defaults, getting values into
  an embedded script, and worked examples.
- **[Configuration](docs/configuration.md)** — every config key, where the model
  and engine are looked for, and the environment variables that exist.
- **[Troubleshooting](docs/troubleshooting.md)** — what each error means and
  what to do about it.

---

## Developing

```sh
make check        # gofmt check, go vet, go test
make test-race    # the concurrency guarantees
make test-short   # skip the tests that need the model
make doctor       # where the pieces are
```

`AGENTS.md` has the hard rules for working in this repository, and
`ARCHITECTURE.md` is the living map of the system — component status, the
dependency graph, a numbered decisions log, and an edge-case register where
every row has a test.

The internal design spec lives in `.dev-docs/` (local, untracked) and is
authoritative. When code and spec disagree, the code changes.
