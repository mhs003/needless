# Configuration

`n` reads `~/.needless/config.json`. Every key is optional, and a missing file is
not an error — its absence is the state of a fresh install.

```json
{
  "command_roots": ["~/.needless/commands"],
  "fallback": "default",
  "weights": "~/.needless/models/needle3.cact",
  "engine": "~/.needless/lib/libneedle.so",
  "system": "date: 2026-07-21 Tue 14:30; locale: en-US"
}
```

**Unknown keys are an error, not ignored.** A misspelled `command_root` would
otherwise silently do nothing, which is the worst way to find out.

`~` is expanded; relative paths are left relative.

## `command_roots`

Directories searched for `.nsc` files, in order. The first root that provides an
ID wins, so a personal directory can shadow a shared or packaged one.

Default: `["~/.needless/commands"]`.

A root that does not exist is fine — that is just a fresh install. A root that
exists but is a *file* is reported, because that is always a mistake.

## `fallback`

The ID of a command to run when no command matches the prompt. Empty or absent
means there is no fallback, and an unmatched prompt simply reports that and exits
1 rather than inventing something.

```json
{ "fallback": "default" }
```

The fallback is an ordinary nscript command, so it can be edited or removed
without touching the CLI. The one difference is that **no model turn selects
it** — the prompt has already been refused, so there is nothing to read argument
values from. It therefore has to give every argument it declares a default:

```nsc
instruction """
Handle a request that matched nothing else.
"""

confirm false

args {
    note: string = "nothing matched"
}

run {
    print("No command matched: " + note)
}
```

If the fallback needs a value it has no default for, `n` says so instead of
running it with a hole in it.

## `weights`

The `.cact` model archive. Set it to skip the search.

That search, when `weights` is unset:

1. `<directory of the n binary>/models/needle3.cact`
2. `<directory of the n binary>/.needless/models/needle3.cact`
3. `./models/needle3.cact`, relative to wherever you ran `n`
4. `~/.needless/models/needle3.cact`

The current directory is checked so that running from a checkout finds its own
`models/`. For an installed `n`, put the archive in `~/.needless/models/` — that
is what `make install-model` does.

## `engine`

The engine shared library. Usually you should not need this: the binding looks
beside the `n` binary, then beside its own package, then in the Python package
cache at `~/.cache/cactus-needle/`. `$NEEDLE_ENGINE` overrides the lot.

v1 ships an engine for **linux-x86_64 only**; see the platform section of
`needle/BINDINGS.md`.

## `system`

Environment facts handed to the model, in the `name: value; name: value` form.
Useful for anything the model cannot know on its own:

```json
{ "system": "date: 2026-07-21 Tue 14:30; locale: en-GB" }
```

If you set nothing, `n` supplies a `date:` fact built from the current time,
because a prompt like "remind me tomorrow" needs a reference date. Setting
`system` replaces that entirely, so include a date yourself if you want one.

## Environment variables

| Variable | Effect |
|---|---|
| `NEEDLE_WORKER` | Path to `needle-worker`, overriding the search beside the `n` binary and on `PATH`. |
| `NEEDLE_ENGINE` | Path to the engine shared library. |
| `NEEDLE_TELEMETRY` | `0` (the default `n` applies) disables the engine's usage reporting. `1` opts back in. |
| `DO_NOT_TRACK` | The general form of the same opt-out. Both are set unless you set them yourself. |
| `EDITOR` / `VISUAL` | Opened by `--new-command` and `--edit-command`. `VISUAL` wins. |
| `HOME` | Where `~/.needless` is. |

Telemetry is **off by default**: `n` sets `NEEDLE_TELEMETRY=0` and
`DO_NOT_TRACK=1` in the engine's environment, but only if you have not set them
yourself. Exporting `NEEDLE_TELEMETRY=1` turns reporting back on.
