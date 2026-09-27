# Needle 3 — Go binding reference

`BINDINGS.md` is the reference for the Go binding to the Cactus Compute
[Needle 3](https://github.com/cactus-compute/needle) on-device model. Read this
before writing code that consumes the binding.

Everything here was verified against the real engine (`libneedle.so` +
`models/needle3.cact`) and against a purpose-built misbehaving C stub. The
§"Landmines" section lists the non-obvious behaviours that cost time if you
guess instead of reading.

---

## 1. Layout

The binding is a normal Go package inside this repository's module
(`github.com/mhs003/needless`). The application imports it as
`github.com/mhs003/needless/needle`.

```
go.mod                     module github.com/mhs003/needless
models/
    needle3.cact           the model archive
needle/
├── needle.go              public API: Config, Needle (New/Complete/Embed/Reset/Close)
├── result.go              typed decoding of a turn's JSON reply (Result, FunctionCall)
├── doc.go                 package documentation
├── BINDINGS.md            this file
├── engine/
│   ├── needle.h           the C ABI header, verbatim from the upstream release
│   └── linux-x86_64/
│       └── libneedle.so   the engine (vendored; see §8 for other platforms)
├── cmd/needle-worker/     the child binary that hosts the engine
└── internal/
    ├── abi/               dlopen + the C ABI trampolines
    ├── worker/            parent/child framing protocol and Worker handle
    └── stubtest/          builds a fake engine + worker for hermetic tests
        └── testdata/stub.c
```

Everything under `needle/internal/` is private to the binding. The supported
surface is `needle.Config`, `needle.Needle`, `needle.Result`, and
`needle.FunctionCall`.

---

## 2. The C ABI

From `engine/needle.h` (unchanged upstream):

```c
int         needle_init(const char* system_prompt, const char* tools_json,
                        const char* tool_index_path);
const char* needle_last_error(void);
int         needle_complete(const char* input, int max_new_tokens,
                            char* out, int out_capacity);
int         needle_embed(const char* input, float* out, int out_capacity);
void        needle_reset(void);
int         needle_load(const unsigned char* cact, unsigned long long n);
```

Verified semantics:

| Call | Returns | Notes |
|---|---|---|
| `needle_load(cact, n)` | non-negative on success, negative on failure | Takes the **raw archive bytes**, not a path. The engine copies what it needs, so the caller's slice need not outlive the call. |
| `needle_init(sys, tools, idx)` | **tokenised length of the static prefix** on success | Fails (negative) when `system` + `tools` do not fit the context window. `tools` may be the compact form or OpenAI `{"type":"function",...}` wrappers. Any of the three args may be `NULL`. |
| `needle_complete(in, max, out, cap)` | negative on failure; otherwise success | `out` receives a **NUL-terminated** JSON string. The non-negative return value is **not** a reliable byte count — see §3.1. |
| `needle_embed(in, NULL, 0)` | the embedding dimension | Dimension probe, no computation. |
| `needle_embed(in, out, cap)` | **number of floats written** | Unlike `complete`, here the count *is* authoritative. |
| `needle_reset()` | void | Clears conversation history; keeps model + prefix. |
| `needle_last_error()` | `const char*` | Process-global. **Invalidated by the next ABI call** — copy it immediately. |

Observed values on `models/needle3.cact`: prefix = 75 tokens,
embedding dimension = **3072**.

The engine owns **one process-global model and one conversation**. It is
**not thread-safe**.

---

## 3. Landmines

These are the things that bite. Each one is covered by a regression test.

### 3.1 `needle_complete`'s return value is not a length

The header only promises "non-negative counts on success". The reference
Python binding (`needle/_worker.py`) ignores the return value entirely and
reads `out` as a C string. So does this binding.

If you treat the return value as a byte count you will silently truncate or
over-read. `internal/abi.Complete` therefore scans `out` for the NUL, and
zeroes the buffer first so that an engine which forgets the terminator cannot
leak bytes from a previous call.

Contrast with `needle_embed`, where the return value *is* the float count.

### 3.2 Never `dlclose` the engine

`libneedle.so` is a C++ shared object with static constructors, so it registers
destructors through `__cxa_atexit`. `dlclose` unmaps it, and the C++ runtime
then runs those destructors **at process exit** against unmapped memory.

The symptom is nasty: every test passes, then the process segfaults during
`exit()`, inside `runtime.racefini` / `atexit`, at an address inside the
(unmapped) library. It only reproduces with the real C++ engine, not with a
plain-C stub, and it is easy to mistake for a test-harness bug.

The engine exposes no teardown entry point, so it is designed to stay mapped
for the process lifetime — exactly how the Python binding (which loads with
`ctypes` and never unloads) behaves. `abi.Engine.Close` is therefore a
*logical* close: it drops the resolved function pointers so every method
returns `ErrNotOpen`, and leaves the library mapped.

### 3.3 The static library is not usable from Go

The HF platform folder ships `libneedle.a`, but it is a **libc++** build
(`_ZNSt3__1...`, `operator new`/`delete` undefined) and Linux distros do not
ship a static libc++ by default:

```
undefined reference to `operator new(unsigned long)'
undefined reference to `std::__1::basic_string<...>::operator='
```

The Python wheel ships `libneedle3.so`, a self-contained shared object whose
only dependencies are `libc`, `libm`, `libpthread`, `libdl`. That is what this
binding vendors and links. **Do not try to link `libneedle.a`.**

### 3.4 One engine per process, one process per model

`needle_load` / `needle_init` / `needle_complete` all operate on process-global
state. Two models cannot coexist in one process, and concurrent calls corrupt
the conversation.

The binding therefore runs one **worker subprocess per `Needle` value**. The
parent holds only a pipe handle. This is why:

- two `Needle` values in one Go program have different PIDs and are isolated;
- a single `Needle` is safe to share across goroutines (calls serialise);
- a crash in the engine kills one worker, not your program.

### 3.5 `last_error` is a borrowed pointer

`needle_last_error()` returns a pointer that the engine owns and invalidates on
the next ABI call. `abi` copies it into a Go string immediately
(`C.GoString`), so the error you hold stays valid.

### 3.6 Empty strings are passed as `NULL`

`cstr("")` returns `NULL` rather than a pointer to an empty C string, matching
the reference binding. All three `needle_init` arguments accept `NULL`.

---

## 4. Wire protocol

Parent and child speak the same framing as the Python worker, byte for byte:

```
[u64 big-endian payload length][UTF-8 JSON payload]
```

on both stdin (parent → child) and stdout (child → parent).

- **Config** (first frame, parent → child): `library`, `weights`, `system`,
  `tools`, `tool_index`, `buffer_size`.
- **Request** (parent → child): `op` ∈ {`complete`, `embed`, `reset`, `close`},
  plus `text` / `max_new_tokens` / `dim`.
- **Response** (child → parent): `status` ∈ {`ready`, `ok`, `error`, `fatal`},
  plus `text` / `embedding` / `dim` / `prefix_tokens` / `message`.

Reading rules (`internal/worker/framing.go`):

- EOF at the start of a header = clean shutdown.
- A truncated header or payload = error, so a mid-write death is not mistaken
  for a graceful exit.
- A length above 1 GiB is refused rather than allocated.

---

## 5. Concurrency model

`ioMu` is held across the **whole write-then-read cycle**, not just the write.
Guarding only the write would let two goroutines interleave and steal each
other's responses. This is asserted by
`TestWorkerConcurrentRequestsStayPaired`.

`Close` and `kill` never take `ioMu`, so a call blocked on a read can always be
unblocked by killing the child. That is what makes context cancellation work:
on `ctx.Done()` the worker is killed, the blocked read unblocks, and the call
returns `ctx.Err()`.

The `context.Context` passed to `Start`, `New` **does not own the child**. It is
honoured only for the startup handshake, so a request-scoped context cannot
kill a long-lived worker later. Cancelling a *call* context does kill the
worker, because the engine has no way to interrupt a generation in flight.

---

## 6. Engine and worker discovery

Engine (`DefaultEnginePath`), in order:

1. `$NEEDLE_ENGINE`
2. `<executable dir>/<libname>`
3. `<executable dir>/engine/<platform>/<libname>`
4. `<needle package>/engine/<platform>/<libname>` (the vendored copy)
5. `~/.cache/cactus-needle/v3/*/<libname>` and `.../<platform>/<libname>`

`<libname>` is `libneedle.so` / `libneedle.dylib` / `needle.dll`.

**`<platform>` is upstream's name, not Go's.** `platformDirs()` maps
`linux/amd64` → `linux-x86_64`, `darwin/arm64` → `macos-arm64`, and so on,
returning both spellings so either layout works. This matters: looking for
`linux-amd64` finds nothing, and on a machine with the Python package installed
the cache silently covers for the mistake. The executable-relative candidates
come first because a shipped binary has no source tree, and the path recorded
by `runtime.Caller` only means anything on the machine that compiled it.

Worker binary, in order:

1. an explicit `Config.WorkerPath`
2. `$NEEDLE_WORKER`
3. `needle-worker` beside the running executable
4. `needle-worker` on `$PATH`

`needle-worker` is built with:

```sh
go build -o needle-worker ./needle/cmd/needle-worker
```

Both binaries belong in the same directory: `n` finds the worker beside itself.
`make build` does the right thing.

---

## 7. Usage

```go
n, err := needle.New(ctx, needle.Config{
    WeightsPath:  "models/needle3.cact",
    ToolsJSON:    tools, // JSON array of schemas
    SystemPrompt: "date: 2026-07-21 Tue 14:30; device: phone",
})
if err != nil { return err }
defer n.Close()

res, err := n.CompleteResult(ctx, "dim the living room to 30", 256)
if err != nil { return err }

if res.Refusal() {
    // off-topic: there is no free-text fallback. Handle it.
    return nil
}
for _, call := range res.FunctionCalls {
    var args map[string]any
    if err := call.Bind(&args); err != nil { return err }
    // ... execute the tool, then feed the result back with another
    // CompleteResult call to continue the same conversation.
}

vec, _ := n.Embed(ctx, "a search query") // for local search / routing
dim, _ := n.EmbedDim(ctx)                // 3072
```

Behaviour contract worth repeating:

- An off-topic request returns **empty `FunctionCalls`** — a refusal, not a
  guess. Always handle the empty case.
- `SuppressedCalls` holds a call the engine withheld (low confidence or a
  grounding gate). Nothing to execute; show it for confirmation.
- `Confidence` is a pointer and is `nil` when the weights carry no calibration
  head (e.g. a local LoRA fine-tune).
- Repeated `Complete` calls continue one conversation. `Reset` clears history
  but keeps tools and model.
- One toolset per `Needle`. To change tools, make a new one.

---

## 8. Platforms

**v1 ships an engine for `linux-x86_64` only**, because that is what the machine
it was built on runs. On any other platform `n` fails immediately with a message
naming the directory it looked for; it does not limp along or guess.

To add a platform, drop the engine under `engine/<platform>/`:

| Target | File |
|---|---|
| `linux-x86_64` | `libneedle.so` (from the Python wheel, `needle/libneedle3.so`) |
| `linux-arm64`, `linux-armv7`, `linux-riscv64`, `linux-mipsel` | `libneedle.so` |
| `macos-arm64` | `libneedle.dylib` |
| `windows-x86_64`, `windows-arm64` | `needle.dll` |

Notes:

- On Linux/macOS the same `dlopen`/`dlsym` trampolines work unchanged.
- **Windows needs a new file, and the build tag of the stub must move with it.**
  `internal/abi/dlopen_other.go` is tagged `!unix`, and Windows is not `unix`, so
  a new `dlopen_windows.go` would collide with it — duplicate definitions of the
  same nine symbols. Narrow the stub to `//go:build !unix && !windows` at the same
  time. `LoadLibrary`/`GetProcAddress` map one-to-one onto the existing
  trampolines, and `FreeLibrary` is **not** wanted: `Close` deliberately never
  unloads (§3.2). Two further snags: paths are UTF-16 on Windows, and
  `engine/needle.h` defines `NEEDLE_API` with a GNU visibility attribute that
  MSVC does not understand (it is guarded by `#ifndef`, so a build can override
  it).
- `needle build --platform <folder> --out <dir>` fetches a platform folder;
  copy the engine from there, or from the Python wheel.

`platformDirs()` in `needle.go` is the one place Go's platform naming is
translated into upstream's. Do not reintroduce `GOOS + "-" + GOARCH` as a path:
it silently finds nothing, and on a machine with the Python package installed the
cache covers for the mistake.

---

## 9. Testing

```sh
cd .          # repo root (module root)
go test ./...            # full suite, hermetic except the real-engine test
go test ./... -race      # the concurrency guarantees
go test ./... -short     # skips the real-engine test
```

Two layers:

**`internal/stubtest/testdata/stub.c`** is a fake engine that implements the
exact ABI and *misbehaves on demand*, driven by substrings in the input:

| Trigger | Behaviour |
|---|---|
| `FAIL_COMPLETE` / `FAIL_EMBED` | return negative and set `last_error` |
| `INIT_FAIL` (in tools) | `needle_init` returns −3 |
| `INIT_BIG_PREFIX` (in tools) | `needle_init` returns 1000000 |
| `TRUNCATE` | fill the buffer, terminate the last byte |
| `NO_TERMINATOR` | fill the buffer and omit the NUL |
| `RETURN_BOGUS` | write `{}` but return 999999 |
| `NUL_EMBEDDED` | write `"a\0b"` — the reader must cut at the NUL |
| `UTF8` | return multi-byte UTF-8 |
| `CRASH` | `abort()` the worker process |
| `SLEEP` | block 2s, for cancellation tests |

This is what makes error paths, truncation and bogus return values testable
without contorting the real engine.

**`TestRealEngineSmoke`** loads the real `libneedle.so` and
`models/needle3.cact`: it checks the prefix length, embedding dimension and a
real generation (`set_lights({"room":"living room","on":true})`). It skips if
the model is absent; point `NEEDLE_MODEL` at an archive to override the path.

If you add ABI behaviour, add a stub trigger and a test — do not rely on the
real engine to exercise an error path.

---

## 10. Known limitations

Deliberate v1 boundaries, not oversights. Each is a decision that could be
revisited; none is a bug waiting to be found.

- **One platform.** An engine is vendored for `linux-x86_64` only (§8). Other
  platforms fail with a message naming the directory that was looked for.
- **Windows dynamic loading is unimplemented**, and adding it needs the stub's
  build tag moved at the same time or the two files collide (§8).
- **No generation cancellation inside the engine.** The C ABI has no interrupt,
  so cancelling a call's context kills the worker and loses the conversation.
  Callers that need to interrupt a turn should expect to rebuild state.
- **Model load is eager and per-worker.** `New` maps the weights — 34 MiB for the
  20-layer archive — and this is the dominant cost of a `Needle`. Measured on the
  development machine, one `n` invocation with five commands takes about 2.5 s
  wall clock, essentially all of it here; the figure grows with the tool schema,
  reaching roughly 12 s with sixty commands, because the whole schema is part of
  the prefix. Reuse one `Needle` rather than creating them per request; a
  long-lived process with a socket protocol would be a different shape of
  program, not a v1 completion.
- **No `needle_build`-style depth selection** in the binding: the depth is a
  property of the `.cact` file you pass in, produced by upstream tooling.
- **Telemetry is off by default** (D52). `Start` sets `NEEDLE_TELEMETRY=0` and
  `DO_NOT_TRACK=1` in the child's environment, but only when those variables
  are not already defined — so exporting `NEEDLE_TELEMETRY=1` opts back in, and
  an empty value counts as a deliberate choice rather than an omission. The
  policy is the pure function `workerEnv`, so it is tested without a process.
