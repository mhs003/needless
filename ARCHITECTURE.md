# Architecture

**This file is the living map of Needless. It must be updated in the same
commit as any change to components, data flow, decisions, or handled edge
cases. A stale ARCHITECTURE.md is a defect (AGENTS.md §5).**

---

## What Needless is

`n` turns a natural-language prompt into the execution of a **known** script.

```
n run the project
      |
      v
  discover .nsc commands        (~/.needless/commands/**/*.nsc)
      |
      v
  Needle 3 selects one command  (intent matching + argument extraction)
      |
      v
  argument defaults + validation
      |
      v
  confirmation policy          (confirm true | false)
      |
      v
  execute the command's run block
      |
      v
  exit code 0 / 1 / 2
```

The model never produces executable text. It selects an index into a known
command list and fills declared argument slots. The `.nsc` file decides what
actually runs (CLI spec §9, AGENTS.md §2).

---

## Known bugs (open)

Read this section before touching anything near it. Each entry records the
symptom, the cause, the decision that has to be made, and where to change the
code. None of these are fixed yet; they are recorded rather than guessed at.

### B1 — A required string argument can be satisfied by an empty string

**Severity:** high. It turns "I could not tell what you meant" into a confusing
failure from inside a shell script.

**Observed:**

```
$ n show me git status
not a git repository:
n: git/status: 21:5: bash exited with status 1
```

**What happened.** The prompt names no project. The model still has to return a
call, so it filled `project` with the empty string. `intent` accepted that as a
supplied value, ran the script, and the script's own guard
(`[ ! -d "$PROJECT/.git" ]`) fired against an empty `$PROJECT`. The user is left
reading a shell-level failure and exit 1 instead of being told which argument
was missing.

**Cause, step by step:**

1. `internal/intent/coerce` accepts `""` for a `string` argument. It is a valid
   Go string, so there is nothing there to reject, and that is arguably right in
   general.
2. `internal/intent/resolveArgs` treats *presence* as *satisfaction*. It errors
   only when an argument is absent (`missing required argument %q`), never when
   it is present but empty.
3. So an argument declared without a default — the syntax that means "this
   script cannot run without a value" — can still silently become `""`.

**Why it matters.** Declaring an argument without a default is the script
author's way of saying the command is meaningless without it. The empty string
is exactly the case where the model had no evidence to offer, and it is exactly
the case that should fall through to the no-match path (and thence to the
fallback) rather than execute. This is the same class of failure as D28 and D29:
a value that cannot be used is worse than a clear failure.

**Decision required before fixing.** Is an empty string ever a meaningful value
for a *required* argument? The recommendation is **no**, with the corollary that
an argument whose author explicitly wrote `x: string = ""` has opted into an
empty value and must keep working. Settle this first; it determines the shape of
the fix.

**Fix sketch (not applied):**

- In `internal/intent/match.go`, in `resolveArgs` — not in `coerce` — after a
  value has been coerced, if the argument has **no default** and the coerced
  value is the empty string, return the existing missing-argument error so the
  caller reports a refusal and takes the fallback path.
- Leave `coerce` alone. Accepting `""` is correct in isolation; the rule is
  about what a required slot means, which is `resolveArgs`' job.
- Decide and document what happens when an argument has a non-empty default and
  the model explicitly supplies `""`. Treating it as "absent" (and so falling
  back to the default) is consistent; treating it as an explicit empty is also
  defensible. Pick one and write it into the decisions table.
- Tests to add, in `internal/intent/match_test.go`:
  - a call supplying `{"project": ""}` for a required string must error and must
    not run;
  - `x: string = ""` must still accept an empty supplied value;
  - an omitted optional string must still take its default.
- Optionally, `internal/commands/examples_test.go` can assert that no shipped
  example declares a required `string` it cannot sensibly receive.

**Do not "fix" this by giving the example a default.** Changing
`examples/commands/git/status.nsc` to `project: string = "."` is a reasonable
change to the example on its own merits — running `git status` in the current
directory is the common case — but it does not fix B1, it only hides one
instance of it. Fix the general rule or say explicitly that you chose not to.

### B2 — Observed, not reproduced: the same prompt matching differently

**Severity:** unknown. Recorded because it was seen, and because it was seen
only once it is not yet a bug report so much as a question.

**Observed.** While exercising the CLI with the shipped examples, the prompt
`start the server for /home/void/needless on port 3000` resolved to
`system/cleanup` on one run and to `project/start` on others; on two runs it
appeared to match nothing at all. The same prompt then resolved to
`project/start` on eight consecutive runs, and `show the git status of …`
resolved to `git/status` on eight of eight. The one-off behaviour predates the
`readResponse` and pipe fixes described under "Fixed this session", so it may
simply have been a misreading of interleaved output from a failing run.

**Why it is plausible anyway.** Matching is a decode, and the tool list, the
`system` facts and the prompt all feed it. `systemFact` generates a `date:` fact
that includes the current minute, so the prefix is not byte-identical between
runs; that is a real source of variation, however small. With five or fewer
commands the retrieval head renders the whole toolset, so retrieval is not the
suspect here.

**What to do next, in order:**

1. Reproduce deliberately: run one prompt fifty times against a fixed command
   set and tally the chosen command. Do not change any code first.
2. If it is stable, close this entry with a note saying so. That is a perfectly
   good outcome and cheaper than an investigation.
3. If it is not, control for the variable that is known to change: pin
   `system` in the config to a fixed string and repeat. If the variance
   disappears, the `date:` fact is the cause and the question becomes whether
   the fact is worth its cost.
4. Only then treat it as a matching-quality problem, which would make it a
   model-behaviour question rather than a Needless bug.

**Do not** paper over this by loosening argument validation or by special-casing
the examples. Whatever the answer, the plumbing behaved correctly in every run:
the CLI executed exactly the command the model returned, and enforced the
confirmation policy of that command.

---

## Fixed this session

Recorded so that they are not re-broken, and so that the reasoning survives.

| Bug | Symptom | Fix |
|---|---|---|
| `readResponse` lost the last frame | Startup reported `child exited unexpectedly` instead of the child's real message, intermittently. | The `case <-w.done` branch could win the race against the reader goroutine and discard a frame already sitting in the pipe. Removed it; the reader's EOF produces the same error, after consuming any buffered frame. Regression test: `TestWorkerKeepsTheFinalFrameWhenTheChildExits`. |
| `Wait()` closed the pipes mid-read | Intermittent `read \|0: file already closed`; `make check` failed roughly one run in twelve. | `StdoutPipe`/`StdinPipe` hand back pipes that `cmd.Wait()` closes, and `Wait` ran from the moment the child started. Replaced with explicit `os.Pipe` passed as `*os.File`, which `Wait` does not touch. |
| Engine discovery was looking in the wrong place, twice | `n` worked on the development machine but failed with `engine library not found` under a clean `HOME`. | The vendored path was resolved one directory too high, and it used Go's platform spelling (`linux-amd64`) where upstream names the directory `linux-x86_64`. Added `platformDirs`, added executable-relative candidates, and made the test fail rather than skip — it had been skipping past both bugs while the Python cache covered for them. |
| `exec` ignored its context | Cancelling a request left the embedded script running. | `exec.Command` replaced with `exec.CommandContext`, and the run loop now checks the context between statements so a script with no `exec` is still interruptible. |
| `parseIf` swallowed the newline after `}` | Any `if` followed by another statement failed to parse. | The `else` lookahead now restores the parser position when there is no `else`. |
| `--edit-command` printed no path without `$EDITOR` | The user was told to set `$EDITOR` but not told which file. | The path is printed before the hint. |

---

## Component map

Status key: **done** · **in progress** · **planned**

| Component | Path | Status | Responsibility |
|---|---|---|---|
| Needle binding | `needle/` | **done** | C ABI access to the model, isolated in a worker subprocess |
| nscript lexer | `internal/nscript/lexer.go` | **done** | `.nsc` source → tokens |
| nscript parser | `internal/nscript/parser.go` | **done** | tokens → AST |
| Command discovery | `internal/commands/` | **done** | find `.nsc` files, build the registry |
| Config | `internal/config/` | **done** | config file, fallback command, command paths |
| Intent | `internal/intent/` | **done** | commands → tool schemas; reply → command + args |
| Runtime | `internal/runtime/` | **done** | execute `run` blocks |
| CLI | `internal/cli/`, `cmd/n/` | **done** | flags, output, exit codes |

---

## Layers

Dependencies point one way only. The graph is a **DAG, not a chain**: `intent`
sits above two packages, and `runtime` and `commands` are siblings that happen
to share `nscript`. This table is generated from the code, not aspirational.

| Layer | Package | Depends on (internal only) |
|---|---|---|
| — | `cmd/n` | `internal/cli` |
| 4 | `internal/cli` | `intent`, `runtime`, `commands`, `config`, `needle` |
| 3 | `internal/intent` | `commands`, `nscript`, `needle` |
| 2 | `internal/runtime` | `nscript` |
| 2 | `internal/commands` | `nscript` |
| 1 | `internal/nscript` | — |
| 1 | `internal/config` | — |
| ext | `needle` | — (imported by `cli` and `intent`) |

```
cmd/n                          process entry, signal handling
 └─ internal/cli               flags, interaction, exit codes
     ├─ internal/intent ─┬─ internal/commands ─── internal/nscript
     │                   ├─ internal/nscript
     │                   └─ needle
     ├─ internal/runtime ─── internal/nscript
     ├─ internal/commands ── internal/nscript
     ├─ internal/config
     └─ needle
```

`internal/nscript` performs no I/O at all: bytes in, AST out.
`internal/config` performs none beyond reading one file and the home directory.
Neither touches the model or starts a process, which is what keeps both fast to
test. Note that `runtime` deliberately does **not** depend on `commands` — it is
handed a parsed `*nscript.Program` and knows nothing about the registry.

Re-verify the table with:

```
go list -f '{{.ImportPath}} -> {{join .Imports ", "}}' ./internal/...
```

---

## Model integration notes

Facts about Needle 3 that shape the code above, recorded so they are not
rediscovered:

- **Tools are the commands.** Each command becomes one tool: name = command ID,
  description = `instruction`, parameters = the `args` schema. This is the whole
  of what the model knows about the command set.
- **More than five tools switches on the retrieval head.** Needle renders only
  the top five tools per turn and constrains the grammar to that subset. It is
  therefore safe to declare many commands, but the top five must be
  distinguishable from their instructions alone. `tool_index_path` persists the
  embeddings so they are not recomputed each run.
- **Confidence may be absent.** It is a calibrated score in [0,1], but weights
  built by local fine-tuning carry no calibration head and report `null`. The
  binding models this as `*float64`, and the CLI must treat a missing score as
  "no opinion" rather than zero.
- **The engine withholds very low confidence calls.** Anything below about 0.1,
  or a call that fails a grounding gate, arrives in `suppressed_calls` with
  `function_calls` empty. This is not an error and must not be executed.
- **There is no free-text answer.** A refused prompt yields an empty call list.
  Needless never asks the model to write prose, and never turns model output
  into a shell command (AGENTS.md §2).

---

## nscript v1 language surface

Derived from `.dev-docs/NSCRIPT.md`. This is the complete set the parser must
handle; anything else is a syntax error.

| Construct | Form | Notes |
|---|---|---|
| Instruction | `instruction """ text """` | free-form prose; the model's only description of the command |
| Arguments | `args { name: type [= default] }` | types: `string`, `int`, `float`, `bool` |
| Confirmation | `confirm true` / `confirm false` | required before running a `run` block |
| Run block | `run { ... }` | the implementation |
| Variable | `let x = expr` | |
| Condition | `if expr { } else { }` | |
| Function | `fn name(a: string) { }` | script-local helper |
| Return | `return` | exits the run block |
| Print | `print(expr)` | |
| Error | `error(expr)` | aborts with a message |
| Environment | `env { NAME = expr }` | exposes nscript values to external scripts |
| External script | `exec <interpreter> <<( ... )<<` | `<<(` … `)<<`; contents are **opaque** |
| Comment | `// to end of line` | no block comments |

---

## Decisions

Each entry records *why*, so it is not re-litigated.

| # | Decision | Rationale |
|---|---|---|
| D1 | The model selects exactly one command; it never emits shell. | Central safety property. CLI spec §9. |
| D2 | `<<( ... )<<` bodies are `[]byte`, never a Go string re-indented or trimmed. | The body is source for another interpreter. Any normalisation corrupts it. |
| D3 | Arguments are typed and validated before execution. | A missing required argument must fail as a usage error, not reach the script as an empty string. |
| D4 | Defaults come from the `args` declaration, applied when the model omits a value. | Spec §7: "Defaults are used when an argument is not supplied." |
| D5 | An unmatched prompt is **not** an error. | Spec §4 and "No-match is a normal state". Triggers the configured fallback or an empty result. |
| D6 | The fallback is an ordinary nscript command selected by config. | Spec §5. It can be edited or removed without touching the CLI. |
| D7 | Confirmation is declared by the script, performed by the CLI. | Spec §8. Keeps the language free of UI concerns. |
| D8 | `n` builds `needle-worker` on demand if missing. | The worker must exist at runtime; asking users to run a second build command is friction. |
| D9 | The lexer is context-sensitive in exactly one place: the word after `exec`. | An interpreter may be a path (`/usr/bin/php`), and `/` is otherwise division. Using the preceding keyword as context is exact; guessing from the text's shape is not. |
| D10 | Triple-quoted strings and `<<( )<<` bodies are stored verbatim — no escape processing, no trimming, no re-indenting. | The block body is source for another interpreter; normalising it corrupts it (AGENTS.md trap). Prose whitespace is harmless to the model. |
| D11 | Newlines are significant tokens; spaces, tabs and `//` comments are not. | Statements are newline-separated with no semicolons (NSCRIPT spec §7). Newline tokens give precise "expected a statement" errors instead of silently gluing two statements together. |
| D12 | Confirmation defaults to **false** when a script does not declare `confirm`. | NSCRIPT spec §14 "Commands that need confirmation declare it" — the policy is opt-in, so a script that says nothing is not gated. |
| D13 | `instruction` and `run` are required; `instruction` must be non-empty. | The instruction is the only thing the model matches against, and the run block is the only thing that executes. A command missing either is unusable, so it fails at parse time (CLI spec §9). |
| D14 | A triple-quoted instruction is trimmed of surrounding whitespace; a plain string literal is not. | The whitespace around `"""..."""` is an artefact of where the delimiters sit. A quoted literal was written deliberately and is taken as-is. |
| D15 | Argument defaults must be literals of the declared type. | A default is applied before execution begins, so it cannot depend on runtime state. Int literals are accepted for `float` because the runtime widens them. |
| D16 | Functions and call arity are checked at parse time. | A misspelled call or wrong argument count is a mistake in the script, not a runtime condition; catching it at parse time reports the line. |
| D17 | Discovery is tolerant: a script that fails to parse is recorded in `Registry.Errors` and skipped, while every other command still loads. | One typo in one file should not disable the whole CLI. The CLI reports the failures on stderr and continues. |
| D18 | Command roots are searched in order and the first root providing an ID wins. | Lets a user's own directory shadow a shared or packaged one without a merge policy. |
| D19 | A command ID is the path relative to its root, without the extension, using forward slashes (`git/status`). | Derived from the filesystem, so it needs no registry, and stable across platforms. |
| D20 | A command root that does not exist is not an error. | A fresh install has no commands yet; that is the ordinary state, not a failure. |
| D21 | Command order is sorted by ID. | The model's tool list must not shuffle between runs, or matching becomes unstable. |
| D22 | The configuration file is JSON at `~/.needless/config.json`. | CLI spec §5 leaves the format implementation-defined. JSON keeps the project on the standard library with no third-party parser. |
| D23 | Unknown configuration keys are an error, not ignored. | A misspelled key in a hand-written file would otherwise silently do nothing, which is the worst possible outcome. |
| D24 | A missing or empty configuration file yields the defaults. | A fresh install has no config file, and that is not a failure. |
| D25 | The `config` package owns every `~/.needless` path; `commands` only walks the roots it is given. | Keeps the layering one-directional: `commands` never imports `config`, and `config` stays free of internal dependencies. |
| D26 | Blank entries in `command_roots` are dropped. | An empty string would otherwise resolve to the working directory and silently pick up stray `.nsc` files. |
| D27 | A command ID is used verbatim as the model's tool name. | Verified against the real engine: a name containing `/` is accepted and returned unchanged, so no sanitising or name-mapping is needed. |
| D28 | An argument the script never declared is an error, not ignored. | Silently dropping it would discard part of what the model understood, and a value that cannot be used is worse than a clear failure. The grammar should make this impossible; if it fires, the toolset and matcher disagree. |
| D29 | Argument coercion is generous where intent is unambiguous (`"3000"` for an int) and strict where it is not (1.5 for an int is refused). | Small models quote numbers and write numbers for strings. Guessing at a fractional integer is not a courtesy, it is a silent wrong answer. |
| D30 | A refusal is a normal result, not an error. | The model signals "nothing here" with an empty call list (CLI spec §4). Turning that into an error would make the ordinary off-topic case look like a crash. |
| D31 | Only the first function call is used. | Needless executes one command per invocation. |
| D32 | `intent` depends on a `Completer` interface, not on `*needle.Needle`. | Matching is then testable against canned replies, with no 34 MiB model and no subprocess. |
| D33 | The intent layer reports `Confidence` but applies no threshold. | The spec says to pick a threshold per product; that is the CLI's policy, not the matcher's. |
| D34 | `+` concatenates two strings or adds two numbers, never mixes them. | The spec's own example writes `string(amount)` rather than relying on implicit conversion. Silently stringifying would hide a mistake that changes what a script means. |
| D35 | An `exec` body is handed to the interpreter on **stdin**, and `env` values through the **environment**. Nothing is interpolated into the body. | NSCRIPT spec §9 asks to avoid textual substitution into shell source. Passing the bytes unmodified and values out-of-band is the strongest form of that (D2). |
| D36 | Two `int`s stay an `int`; anything involving a `float` widens. `10 / 3` is `3`, `10.0 / 4.0` is `2.5`. Division by zero is an error. | Predictable and matches what a small orchestration language should do; there is no surprise rounding. |
| D37 | The run block is a single scope. Functions can read it, and their own locals do not leak out. | A helper can use a `let` from the script without threading it through parameters, and there is no accidental capture in the other direction. `if` blocks share the enclosing scope. |
| D38 | Recursion is capped at 64 frames. | v1 has no loop construct, so an unbounded `fn` is the one way to exhaust the Go stack. A clear error beats a crash. |
| D39 | A child's non-zero exit is a command execution failure carrying that code. | CLI spec §13 gives exit 1 that meaning; the code is preserved so the CLI can pass it on if it wants to. |
| D40 | `exec` runs under `CommandContext`. | Without it, cancelling the caller's context would leave a long-running embedded script running. Found by a test, not by inspection. |
| D41 | A child's stdout and stderr stream straight through, unbuffered. | Output from an embedded script appears as it happens; buffering it would make a slow command look hung. |
| D42 | A configured fallback runs with its own declared defaults; nothing selects it, so it must declare a default for every argument it needs. | The fallback is "a normal nscript command selected by configuration" (CLI spec §5), but no model turn chose it, so there is no output to read values from. A second model pass just to fill a rarely-used command was judged not worth a second 34 MiB worker. |
| D43 | An unmatched prompt, and a declined confirmation, both exit 1. | CLI spec §13 fixes 0/1/2 and forbids new meanings for v1. Neither case is a usage error, and reporting success would let `n cleanup && …` proceed as though something ran. The spec explicitly allows a distinct code later. |
| D44 | When the configuration names no `system` facts, Needless supplies a `date:` fact. | Mirrors the reference Python binding. A prompt like "remind me tomorrow" needs a reference date and the model has no other way to obtain one. |
| D45 | An empty `--list-commands` is exit 0, with a hint on stderr. | It is a successful query, like `ls` on an empty directory. The hint keeps a fresh install from looking like silence. |
| D46 | A missing `$EDITOR` is not an error: the path is printed instead. | `n --edit` did what it could; failing would imply the user did something wrong. |
| D47 | Needless' own messages go to stderr; stdout carries only the command's output. | Keeps `n <prompt> | ...` clean. A confirmation prompt is interaction, not output. |
| D48 | The confirmation never echoes argument values. | A script may declare a password (NSCRIPT spec §14). The command id and the first line of the instruction are enough to decide. |
| D49 | `internal/cli` takes its streams and a model factory as inputs, and `Main` never calls `os.Exit`. | The whole CLI, including the prompt, confirmation and fallback paths, is then testable without a process or the model. |

---

## Edge cases

Every entry here has a test. Adding a row without a test is incomplete work
(AGENTS.md §6).

### Binding (`needle/`)

| Case | Behaviour |
|---|---|
| `needle_complete` returns a bogus non-negative value | Ignored; output is read as NUL-terminated. |
| Engine writes no NUL terminator | Buffer is treated as fully filled; no over-read. |
| Embedded NUL in output | String is cut at the first NUL. |
| `dlclose` the engine | Never done. C++ static destructors would run at exit against unmapped memory. |
| Engine returns negative | `last_error` is copied immediately and returned as a Go error. |
| Child process crashes mid-call | Call fails fast; it does not hang. |
| Two `Needle` values | Distinct worker processes, fully isolated models and conversations. |
| Concurrent calls on one `Needle` | Serialised; responses stay paired with requests. |

Full list and detail: `needle/BINDINGS.md`.

### Lexer (`internal/nscript`)

| Case | Behaviour |
|---|---|
| `)<<` appears inside an embedded script body | Terminates the block at the first occurrence. nscript never inspects the body, so a body needing a literal `)<<` is not expressible in v1. |
| `exec` with no interpreter (`exec <<(x)<<`) | Error: `expected an interpreter after "exec"`. |
| `exec` followed by a newline | The interpreter is required on the same line; the parser reports it. |
| Interpreter is a path (`/usr/bin/php`) | Lexed as one token, because `exec` sets the context. `<=` / `>>` elsewhere are still operators. |
| `1.foo` | `1` `.` `foo`, not a malformed float — a `.` only continues a number when a digit follows. |
| Multi-line block, then a later error | The block advances the line counter, so subsequent positions and errors stay correct. |
| CRLF source | `\r` is whitespace; `\n` still ends the line. |
| Non-ASCII source | Columns count runes, so positions match editor columns. |
| Unknown escape (`"\q"`) | Error, rather than silently dropping the backslash. |
| Unterminated `"`, `"""`, or `<<(` | Error naming the construct, with the opening position. |
| Keyword used as a prefix (`runner`, `lets`) | A whole-word identifier, not a keyword. |
| Empty / whitespace-only / comment-only source | Lexes to newlines then EOF; never an error. |

### Parser (`internal/nscript`)

| Case | Behaviour |
|---|---|
| Missing `instruction` or `run` | Parse error naming the missing section. A script with either missing cannot be used. |
| Empty instruction (`instruction ""`) | Parse error; an instruction the model cannot match on is useless. |
| Duplicate section (`run` twice, `args` twice, …) | Parse error naming the section. |
| `else` on the line after `}` | Accepted. The lookahead restores the position when there is no `else`, so the newline still terminates the `if` statement. |
| `else if` chain | Normalised into a `Block` holding one `IfStmt`, so consumers only ever see `*Block` for an else. |
| Statement not followed by a newline (`print("a") print("b")`) | Parse error; two statements on one line are never silently accepted. |
| Bare expression as a statement (`1 + 2`) | Parse error; it would have no effect. |
| `fn print()` / `fn error()` | Parse error: builtin names are reserved, because they are keywords. |
| Duplicate `fn` name | Parse error, pointing at the first declaration. |
| Call to an undeclared function (`prnt(x)`) | Parse error at parse time, not a runtime surprise. |
| Wrong arity, builtin or user `fn` | Parse error. For a user `fn` with defaults, the accepted range is reported. |
| Default that is not a literal (`p: int = other`) | Parse error; a default cannot depend on runtime state. |
| Default of the wrong type (`p: int = "s"`) | Parse error naming both types. `p: float = 3` is allowed (int widens). |
| Integer literal too large for `int64` | Parse error from the number conversion. |
| `exec` without a `<<( ... )<<` block | Parse error naming the interpreter. |
| Everything inside `<<( ... )<<` | Never parsed. The bytes are carried in `ExecStmt.Body` untouched. |

### Command discovery (`internal/commands`)

| Case | Behaviour |
|---|---|
| Root does not exist | Empty registry, no error — the ordinary state of a fresh install. |
| Root is `""` or blank | Ignored. |
| Root directory is empty | Empty registry, no error. |
| Hidden file (`.hidden.nsc`) or hidden directory (`.git/`) | Skipped, so editor and VCS droppings never become commands. |
| Hidden *root* (e.g. `~/.needless`) | Still walked; only entries below the root are filtered. |
| Non-`.nsc` file (`notes.txt`, `x.nsc.bak`) | Skipped. |
| Nested directories | Walked to any depth; ID keeps the path (`a/b/c/deep`). |
| A root that is a file, not a directory | Handled without panic. |
| Unreadable file (permissions) | Recorded as a `LoadError`; the rest of the root still loads. |
| One script fails to parse | Recorded with its path and the syntax error; every other command still loads (D17). |
| Same ID under two roots | The earlier root wins (D18). |
| Ordering | Sorted by ID, identical across runs (D21), so the model's tool list is stable. |
| `Commands()` / `Errors()` | Return copies; a caller cannot mutate the registry's state. |

### Config (`internal/config`)

| Case | Behaviour |
|---|---|
| File does not exist | Zero `Config` and no error; `Roots()` then yields `~/.needless/commands` (D24). |
| File is empty or whitespace | Treated the same as missing. |
| `Load("")` | Reads the default location. |
| Unknown key (`command_root`) | Error naming the key, so a typo cannot silently do nothing (D23). |
| Malformed JSON, or a value of the wrong type | Error quoting the file path. |
| Two JSON documents in one file | Error: it is almost always a truncated edit. |
| `~/...` in `command_roots` | Expanded to the home directory. |
| A `~` that is not leading (`/a/~/b`) | Left alone; only a leading tilde expands. |
| Blank entry in `command_roots` | Dropped, so it cannot resolve to the working directory (D26). |
| Unreadable file (permissions) | Error wrapping `fs.ErrPermission`, so callers can branch on it. |
| `HOME` unset | Error from `Dir()`, rather than silently using a relative path. |

### Intent (`internal/intent`)

| Case | Behaviour |
|---|---|
| Prompt matches nothing (refusal) | `Matched=false` and no error; the caller falls back per config (D30). |
| Engine withholds a low-confidence call | Reported in `Suppressed`, `Matched=false`; nothing is executed. |
| Model names a command that was not declared | Error. The grammar should make this impossible, so it means the toolset and matcher disagree (D28). |
| A required argument is absent | Error naming the argument; the script is never run with a missing value. |
| The model supplies an undeclared argument | Error naming it (D28). |
| Several function calls in one reply | The first is used, the rest ignored (D31). |
| Argument quoted as a string (`"3000"`) | Coerced to the declared numeric type. |
| Number supplied for a string argument | Rendered without a trailing `.0`, so `3000` reads as `3000`. |
| Fractional value for an `int` argument | Error. Rounding would be a silent wrong answer (D29). |
| `null`, an array, or an object as an argument | Error; a declared primitive has no meaningful reading of those. |
| No commands declared | `ErrNoCommands`, returned before the model is invoked at all. |
| Default of `false`, `0`, or `""` | Still emitted into the schema. A plain `omitempty` would drop exactly the defaults most worth stating. |
| Tool schema order | Sorted by command ID and byte-identical between runs (D21). |

### Runtime (`internal/runtime`)

| Case | Behaviour |
|---|---|
| `+` mixing a string and a number | Error naming both types; there is no implicit stringification (D34). |
| `10 / 3` and `10.0 / 4.0` | `3` and `2.5`; two ints stay ints (D36). |
| Division by zero, int or float | Error, rather than an infinity or a panic. |
| `1 == 1.0` | True; int and float compare numerically. |
| Comparing a string with a number using `<` | Error, rather than a surprise ordering. |
| `&&` / `\|\|` | Short-circuit; the right operand is not evaluated when the left decides it. |
| A function's local | Not visible outside it. |
| A `let` in the run block | Visible inside functions and inside `if` blocks. |
| A self-recursive `fn` | Stopped at 64 frames with a clear error (D38). |
| `return` in the run block | Ends the run cleanly, not as a failure. |
| `return` inside a function | Ends that function; the caller continues. |
| `error("...")` | Aborts with the message at the statement's position; nothing after it runs, and anything printed before it is kept. |
| Undefined variable or a type error | Error carrying the line and column. |
| `print` output | Goes to the executor's `Out`; nil discards rather than panicking. |
| `env` binding referenced as a variable | Error: env values feed the child process, they are not nscript variables. |
| The same env name set twice | The later value wins. |
| Env values | Rendered from string/int/float/bool; a composite would be a parser-level impossibility. |
| The body containing `$VAR` or `$name` | Untouched; the shell expands what it recognises, nscript expands nothing (D35). |
| Child exits non-zero | Error carrying the exit code; output produced before the failure is already on stdout (D39). |
| Interpreter not on `PATH` | Error naming the interpreter. |
| Empty `<<()<<` body | Runs the interpreter with empty stdin; not an error. |
| Context cancelled mid-`exec` | The child is killed and the error says so (D40). |
| Child stdout / stderr | Streamed live to `Out` / `Err` (D41). |
| `Executor.Dir` | Sets the child's working directory. |
| A missing argument value in `args` | Error: the matcher always supplies defaults, so this is a caller bug, not a user one. |

### CLI (`internal/cli`, `cmd/n`)

| Case | Behaviour |
|---|---|
| No arguments | Usage error, exit 2, with a pointer to `--help`. |
| Unknown option | Exit 2, naming the option. |
| Extra argument after `--help` / `--list-commands` | Exit 2; it is almost always a mistake. |
| `--` | Ends options; everything after is prompt text. |
| `n run the project` and `n "run the project"` | The same prompt; trailing words are joined with spaces. |
| Prompt with no commands installed | Exit 1, naming the roots searched. |
| One command fails to parse | Warning on stderr; the rest still work (D17). |
| Model matches a command | The command runs; stdout is the command's output only (D47). |
| Model refuses | Exit 1, with the model's reasoning on stderr. |
| Model withholds a low-confidence call | Reported as withheld; nothing runs. |
| Refusal with a fallback configured | The fallback runs with its declared defaults (D42). |
| Fallback configured but not a known command | Exit 1, naming it. |
| Fallback needs an argument it has no default for | Exit 1, explaining why it cannot be used as a fallback (D42). |
| Fallback with no arguments at all | Runs; the empty default set is valid. |
| `confirm true`, user answers `y`/`yes` | Runs. |
| `confirm true`, anything else | Exit 1, nothing runs. |
| `confirm true`, no terminal (EOF on stdin) | Treated as declining — the safe reading of no answer. |
| `confirm true`, arguments include a password | The values are not echoed (D48). |
| `confirm false` or not declared | Runs without asking (D12). |
| Command's run block fails | Exit 1; the failing position and the child's exit status are on stderr. |
| Command output before a failure | Already on stdout and kept. |
| `--list-commands` with no commands | Exit 0 with a hint on stderr (D45). |
| `--new-command <id>` | Creates `<root>/<id>.nsc` from a template that parses. |
| `--new-command` with no id | Asks on stderr, reads the answer from stdin. |
| `--new-command` where the file exists | Exit 1; refuses to overwrite. |
| `--new-command` with a bad id (`../x`, `/abs`, `a/./b`, `-`) | Exit 2; the id never becomes a path unchecked. |
| `--new-command` with `$EDITOR` set | Opens the template in it. |
| `--edit-command <id>` | Prints the path and opens it in `$VISUAL`/`$EDITOR`. |
| `--edit-command` with no id | Lists the commands and reads a number from stdin. |
| `--edit-command` with an unknown id | Exit 1. |
| `$EDITOR` unset | Prints the path; not an error (D46). |
| Ctrl-C during a command | The context is cancelled; the child stops and the script stops between statements. |

### Application

---

## Testing strategy

- **Pure unit tests** for `internal/nscript` (lexer, parser, call checking),
  `internal/config`, and `internal/commands`: no model, no subprocesses, fast
  enough to run on every save.
- **Table-driven tests** throughout, with malformed input asserted to produce a
  useful line/column error rather than merely "an error".
- **Fake model** for `internal/intent` and `internal/cli`: matching is tested by
  injecting canned replies through the `Completer` interface, so no test needs
  the 34 MiB model. The CLI takes its streams and a model factory as inputs, so
  the whole prompt, confirmation and fallback path is exercised hermetically.
- **Real subprocesses** for `internal/runtime`: `exec` blocks run against the
  machine's `sh`, covering exit codes, stderr routing, cancellation, working
  directory, and the fact that nothing is interpolated into a body.
- **A guard on the shipped examples** (`internal/commands/examples_test.go`):
  every example must parse, must be discoverable, must be usable as a fallback,
  and between them they must use every construct the language has. Examples are
  documentation, and an example that does not parse is worse than none.
- **Stub engine** (`needle/internal/stubtest`) for the binding's error paths,
  driven by trigger substrings such as `FAIL_COMPLETE` and `TRUNCATE`.
- **One real-model test**, `needle.TestRealEngineSmoke`, which loads
  `models/needle3.cact` and is skipped when the model is absent. Point
  `NEEDLE_MODEL` elsewhere to override the path.

A test that skips when the thing under test is broken gives false confidence.
`TestDefaultEnginePathFindsVendoredEngine` used to skip when discovery failed
and thereby hid two real bugs; it now fails if the vendored engine it knows is
committed cannot be found.

`gofmt -l .`, `go vet ./...` and `go test ./...` must all be clean before a
commit, with no skipped tests beyond the ones that legitimately require the
model (AGENTS.md §4). `make check` runs all three.

---

## Environment

| Thing | Value |
|---|---|
| Module | `github.com/mhs003/needless` |
| Language | Go. cgo appears in exactly one file, `needle/internal/abi/dlopen_unix.go`, which loads the engine; nothing in the application layer uses it. |
| Model | `models/needle3.cact` — the published full-depth archive, 34 MiB, 20 layers, 3072-dim embeddings (untracked) |
| Engine | `needle/engine/linux-x86_64/libneedle.so` (vendored, 1.3 MiB, self-contained) |
| Command root | `~/.needless/commands/` |
| Internal spec | `.dev-docs/` — local and untracked; the source of truth for behaviour |
| User docs | `docs/` — for material aimed at users of `n` (not yet present) |

---

## Related documents

- `AGENTS.md` — hard rules for agents working here.
- `README.md` — quick start for users of `n`, and a summary of the language.
- `.dev-docs/CLI.md` — authoritative CLI specification (local, v1).
- `.dev-docs/NSCRIPT.md` — authoritative nscript specification (local, v1).
- `needle/BINDINGS.md` — the C ABI, its traps, and the binding's API.
- `docs/` — longer user-facing documentation, kept separate from the spec.
