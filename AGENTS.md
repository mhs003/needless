# AGENTS.md

Hard rules for any agent working in this repository. These are not suggestions.
If a rule blocks you, stop and ask — do not work around it.

## What this project is

**Needless** is a local CLI named `n`. It maps a natural-language prompt to a
user-defined **nscript** command (a `.nsc` file), extracts that command's
arguments, and executes the command's `run` block.

The natural-language understanding is done by **Needle 3**, an on-device model,
through the C ABI binding in `needle/`.

Two specs define v1 and are authoritative:

- `.dev-docs/CLI.md` — the `n` command-line interface
- `.dev-docs/NSCRIPT.md` — the nscript language (`.nsc` files)

## Hard rules

### 1. The spec is read-only

`.dev-docs/CLI.md` and `.dev-docs/NSCRIPT.md` are **v1 and must not be modified**. They
are deliberately untracked (see `.git/info/exclude`); they exist only on this
machine.

The command language's file extension is **`.nsc`**. (Revised from `.nls` in v1
to avoid colliding with Windows gettext `.nls` files.)

Agents never edit the spec. Only the project owner may revise it, and a
revision is applied deliberately, never as a side effect of making code work.

When code and spec disagree, **change the code**. If you believe the spec itself
is wrong, say so and stop — never silently edit it, and never treat "the spec
didn't mention it" as licence to invent behaviour.

### 2. The model never writes executable code

The model may only ever:

- choose **one** command from the set of discovered `.nsc` files, and
- fill in the arguments that the chosen command **declares**.

It must never produce a shell string, a command line, or a code fragment that
is then executed. Natural language selects a known script; the script decides
what runs. This is the project's central safety property (CLI spec §9). Any
change that lets model output reach an interpreter is a bug, not a feature.

### 3. Go only, and one door to the engine

The application is **pure Go**. No cgo, no C++, no Python, no shelling out to
the upstream `needle` CLI or its Python API.

cgo appears in exactly one file, `needle/internal/abi/dlopen_unix.go`, whose job
is loading the engine's shared library. That file is the exception, not a
precedent: do not add cgo anywhere else.

The `needle` package is the **only** way to reach the model. Do not re-bind the
ABI elsewhere, do not add a second engine path, do not call `libneedle`
directly from application code.

### 4. Every phase ends green, then committed

Before committing:

```
gofmt -l .          # must print nothing
go vet ./...        # must be clean
go test ./...       # must pass
```

`make check` runs all three. `make build` produces `n` and `needle-worker`;
`make doctor` reports where the pieces are.

Then commit. One phase per commit, focused and reviewable. Commit messages end
with the required co-author trailer and explain **why**, not just what.

Never commit a red tree. Never leave a phase half-migrated.

### 5. Keep ARCHITECTURE.md true

`ARCHITECTURE.md` is the living map of the system. Any change to components,
data flow, decisions, or handled edge cases must update it **in the same
commit** as the code. A stale ARCHITECTURE.md is a defect. See the maintenance
contract at the top of that file.

Its **Known bugs** section is a register of open defects, each with the symptom,
the cause, the decision that has to be made and where to change the code. Read
it before working near anything it names. When a bug is fixed, move it to
"Fixed this session" with a line about the fix — do not just delete it.

### 6. Discovered edge cases get recorded

When you find a real edge case, it goes in `ARCHITECTURE.md` under "Edge cases"
**and** gets a test. Undocumented behaviour will be re-broken later.

## Code rules

- **Tests ship with the code.** No new package without tests for it.
- **No comments that restate the code.** Comment the *why* — a non-obvious
  constraint, a spec rule, a trap. A comment explaining what the next line
  obviously does is noise.
- **Wrap errors with `%w`** so callers can use `errors.Is`/`errors.As`.
- **stdout is for the user; stderr is for diagnostics.** Never print progress
  chatter to stdout — the CLI's output is part of its contract.
- **Exit codes are API.** `0` success, `1` command execution failure, `2`
  CLI usage error (CLI spec §13). Do not invent new meanings.
- **No backwards-compatibility shims.** This is v1; change the code.
- Keep the language small. nscript is an orchestration language, not a
  general-purpose one.

## Layout

```
go.mod                      module github.com/mhs003/needless
Makefile                    build, test, install and diagnostics entry points
README.md                   user-facing quick start and language summary
.dev-docs/                  the v1 spec: CLI.md, NSCRIPT.md (local, untracked)
docs/                       longer user-facing documentation (not yet present)
models/                     needle3.cact (untracked, 34 MiB)
needle/                     the Needle 3 Go binding (self-contained; see needle/BINDINGS.md)
cmd/n/                      the `n` executable; wiring only
internal/cli/               flag parsing, interaction, output, exit codes
internal/intent/            model integration: prompt -> command + arguments
internal/runtime/           executes a command's run block
internal/commands/          discovery and the command registry
internal/config/            configuration, including the fallback command
internal/nscript/           lexer + parser + AST for .nsc files
examples/commands/          example .nsc commands, guarded by a test
AGENTS.md                   this file
ARCHITECTURE.md             living system map
```

The order above is the dependency order: everything points downward. `cmd/n` is
deliberately thin — it wires signals and streams and calls `internal/cli`, so
that every decision the CLI makes is testable without a process.

`.dev-docs/` is the internal design spec — the source of truth for behaviour.
`docs/` is for material aimed at users of `n`. Never merge the two: the spec is
authoritative and versioned separately from anything user-facing.

## Traps

Short list of things already known to cost time. Full detail in
`ARCHITECTURE.md` and `needle/BINDINGS.md`.

- **`<<( ... )<<` blocks are opaque.** The text between the delimiters is
  source for *another* interpreter. Hand it through byte-for-byte. Never
  re-indent, trim, or interpolate into it.
- **The engine is process-global and not thread-safe.** One `needle.Needle` per
  process is the supported shape; the binding already isolates each value in a
  worker process.
- **Never `dlclose` the engine.** See `needle/BINDINGS.md` §3.2.
- **An off-topic prompt is a normal outcome, not an error.** The model refuses
  by returning an empty call list. Handle it; fall back per config.
- **`needle-worker` must exist at runtime.** Build it once with
  `make build`, or `go build ./needle/cmd/needle-worker`. It is found beside
  the `n` binary, so the two belong in the same directory.
- **Upstream platform directory names are not Go's.** The engine lives under
  `engine/linux-x86_64/`, not `engine/linux-amd64/`. `needle.platformDirs`
  translates; do not reintroduce `GOOS+"-"+GOARCH` as a path.
