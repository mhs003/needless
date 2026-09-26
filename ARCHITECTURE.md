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
| Runtime | `internal/runtime/` | planned | execute `run` blocks |
| CLI | `internal/cli/`, `cmd/n/` | planned | flags, output, exit codes |

---

## Layers

Each layer has exactly one job and depends only on the layer beneath it.

```
cmd/n                 process entry; wires flags to cli
  internal/cli        flag parsing, user interaction, exit codes
    internal/intent   prompt -> (command, args)          [needs needle]
    internal/runtime  execute a run block                [needs commands+nscript]
      internal/commands  discovered .nsc files           [needs nscript]
        internal/config  where things live, fallback     [needs nothing]
        internal/nscript lexer + parser + AST            [needs nothing]
          needle         the model
```

`internal/nscript` and `internal/config` are pure: no I/O beyond reading files
they are given, no model, no process execution. That keeps them fast to test.

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
| D32 | `intent` depends on a `Completer` interface, not on `*needle.Needle`. | Matching is then testable against canned replies, with no 29 MiB model and no subprocess. |
| D33 | The intent layer reports `Confidence` but applies no threshold. | The spec says to pick a threshold per product; that is the CLI's policy, not the matcher's. |

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

### Application

Not yet implemented — rows are added as each phase lands.

---

## Testing strategy

- **Unit tests** for `internal/nscript` and `internal/config`: pure, no model,
  no subprocesses. Fast enough to run on every save.
- **Table-driven tests** for the lexer and parser, with malformed-input cases
  asserted to produce useful line/column errors.
- **Fake model** for `internal/intent`: the intent layer is tested by injecting
  canned replies, so tests never depend on the 29 MiB model or inference.
- **Stub engine** (`needle/internal/stubtest`) for the binding's own error
  paths.
- **One real-model end-to-end test**, skipped when the model is absent.

`go test ./...` must pass without the model present except for the explicit
end-to-end test.

---

## Environment

| Thing | Value |
|---|---|
| Module | `github.com/mhs003/needless` |
| Language | Go (pure; no cgo in the application layer) |
| Model | `models/needle3.cact`, 20 layers, 3072-dim embeddings (untracked) |
| Engine | `needle/engine/linux-x86_64/libneedle.so` (vendored, self-contained) |
| Command root | `~/.needless/commands/` |
| Internal spec | `.dev-docs/` — local and untracked; the source of truth for behaviour |
| User docs | `docs/` — for material aimed at users of `n` (not yet present) |

---

## Related documents

- `AGENTS.md` — hard rules for agents working here.
- `.dev-docs/CLI.md` — authoritative CLI specification (local, v1).
- `.dev-docs/NSCRIPT.md` — authoritative nscript specification (local, v1).
- `needle/BINDINGS.md` — the C ABI, its traps, and the binding's API.
- `docs/` — user-facing documentation, kept separate from the internal spec.
