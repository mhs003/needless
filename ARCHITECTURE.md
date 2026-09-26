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
| nscript lexer | `internal/nscript/lexer.go` | planned | `.nsc` source → tokens |
| nscript parser | `internal/nscript/parser.go` | planned | tokens → AST |
| Command discovery | `internal/commands/` | planned | find `.nsc` files, build the registry |
| Config | `internal/config/` | planned | config file, fallback command, command paths |
| Intent | `internal/intent/` | planned | commands → tool schemas; reply → command + args |
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

## nscript v1 language surface

Derived from `.docs/NSCRIPT.md`. This is the complete set the parser must
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
| Spec | `.docs/` — local and untracked |

---

## Related documents

- `AGENTS.md` — hard rules for agents working here.
- `.docs/CLI.md` — authoritative CLI specification (local, v1).
- `.docs/NSCRIPT.md` — authoritative nscript specification (local, v1).
- `needle/BINDINGS.md` — the C ABI, its traps, and the binding's API.
