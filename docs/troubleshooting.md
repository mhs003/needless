# Troubleshooting

Everything `n` says about itself goes to **stderr**. The command's own output is
the only thing on **stdout**, so if you are piping `n` somewhere, the diagnostic
you are looking for is not in the pipe.

Check the installation first:

```sh
make doctor
```

It reports where the commands, the model, the engine and `needle-worker` are, and
which are missing.

## `n: command not found`

The binary is not on your `PATH`. `make install` puts it in `~/.local/bin`, which
may not be on your `PATH` yet.

## `n: no commands found in ...`

It lists the directories it searched. Either you have not written any commands
yet:

```sh
make install-examples     # copies the examples into ~/.needless/commands
n --new-command hello
```

or `command_roots` points somewhere unexpected. See
[configuration](configuration.md).

Note that this is exit 0 for `n --list-commands` — an empty listing is a
successful query — but exit 1 when it stops a prompt from running.

## `n: no command named "..."`

The ID does not match anything. IDs are paths below the command directory without
the extension: a file at `~/.needless/commands/git/status.nsc` is `git/status`.

## `n: skipping /path/to/file.nsc: ...`

That command failed to parse and was left out; everything else still works. The
message names the file and the line and column. Common causes:

- `instruction` or `run` missing entirely — both are required.
- An unterminated `"""` or `<<( ... )<<`.
- An argument with an unknown type, or a default of the wrong type.

`n --show-command <id>` prints the file if you want to look at it.

## `worker: cannot find needle-worker`

`n` runs the engine in a helper process, and it looks for `needle-worker` beside
the `n` binary, then on your `PATH`, then at `$NEEDLE_WORKER`. Build both
together:

```sh
make build
```

They belong in the same directory.

## `needle: engine library not found for ...`

The engine is not where it was looked for, or your platform has no engine in
this build. v1 ships one for **linux-x86_64**; other platforms will say so
plainly rather than failing oddly. Point `engine` in the config or
`$NEEDLE_ENGINE` at a library to use your own.

## `n: no model archive found`

The 34 MiB `.cact` archive was not found. Either put it where `n` looks (see
[configuration](configuration.md)), or set `weights`:

```json
{ "weights": "/path/to/needle3.cact" }
```

`make install-model` copies the one from the checkout into
`~/.needless/models/`.

## `n: <command> needs a value for <argument>`

The prompt named a command but not a value it requires. The command did **not**
run. Say the missing thing:

```
$ n show me the weather
n: weather needs a value for city, and the prompt does not provide one

$ n what is the weather in Paris
weather for Paris
```

If a value is genuinely optional, give it a default in the script.

## `n: no command matched the prompt`

The model did not recognise the prompt as anything you have defined. That is a
normal outcome, not a failure — an off-topic question should not turn into a
shell command. Options:

- Rephrase using words closer to a command's `instruction`.
- Add or improve a command's instruction to cover the phrasing.
- Configure a `fallback` so something helpful happens instead.

If the model made a low-confidence guess, the CLI says so and names the command
it nearly chose.

## `n` picked the wrong command

The `instruction` is the only thing being matched, so that is where to fix it:

- Put the words a user would actually type into the instruction.
- If two commands overlap, say in each instruction how they differ.
- `n --list-commands` shows every instruction as the model sees it.

Matching is done on-device and is not perfect; it is a small model choosing
between the commands you wrote, not a general assistant. See
[writing commands](writing-commands.md).

## The command ran but failed

`n: <command>: <line>:<col>: bash exited with status 1` means the embedded script
failed. The position is in the `.nsc` file, not in the script. Run the body
directly to debug it, remembering that values arrive through the environment:

```sh
PROJECT=/tmp/demo bash -c 'cd "$PROJECT" && ls'
```

Output printed before the failure is kept — it is not rolled back.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | the command failed, or nothing was run |
| `2` | the invocation was wrong |

An unmatched prompt and a declined confirmation are both 1, because nothing ran.
That is what makes `n cleanup && echo done` safe.

## Seeing what the model thought

On a refusal, or when an argument could not be filled, `n` prints the model's
own reasoning to stderr. It is not a conversation — the model only ever chooses a
command and fills its arguments — but it often explains the choice.

## The test suite takes minutes

Expected. The real-model tests load a 34 MiB archive each and run inference on
the CPU. Use `make test-short` while working, and `make test-e2e` when you want
only the slow layer.
