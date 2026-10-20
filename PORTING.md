# LeanInteract → Go / TypeScript porting spec

Source: https://github.com/augustepoiroux/LeanInteract (Python, v0.11.6, REPL default `v1.3.19`
from `https://github.com/augustepoiroux/repl`).

Layout:

```
go/   Go module   github.com/ivigasin/lean-interact/go   (package leaninteract, cmd/lean-interact)
ts/   npm package lean-interact                             (ESM + .d.ts, bin lean-interact)
```

Both ports have **zero third-party runtime dependencies**. Git is driven through the `git` CLI,
file locks are implemented in-house, process memory is read from the OS.

## Module mapping

| Python                         | Go                                   | TypeScript                       |
|--------------------------------|--------------------------------------|----------------------------------|
| `interface.py`                 | `interface.go`, `infotree.go`        | `src/interface.ts`               |
| `config.py` `LeanREPLConfig`   | `config.go` `NewConfig(ctx, opts)`   | `src/config.ts` `LeanREPLConfig.create(opts)` |
| `project.py`                   | `project.go`                         | `src/project.ts`                 |
| `server.py` `LeanServer`       | `server.go` `Server`                 | `src/server.ts` `LeanServer`     |
| `server.py` `AutoLeanServer`   | `autoserver.go` `AutoServer`         | `src/server.ts` `AutoLeanServer` |
| `sessioncache.py`              | `sessioncache.go`                    | `src/sessioncache.ts`            |
| `pool.py` `LeanServerPool`     | `pool.go` `Pool`                     | `src/pool.ts` `LeanServerPool`   |
| `utils.py` (string helpers)    | `utils.go`                           | `src/utils.ts`                   |
| `utils.py` (git, lake, memory) | `internal` helpers in `git.go`, `sys_*.go`, `filelock_*.go` | `src/internal/*.ts` |
| `install-lean`, `clear-lean-cache` scripts | `cmd/lean-interact install-lean|clear-cache` | `lean-interact install-lean|clear-cache` |

## Cross-language decisions

* **Wire format is the contract.** JSON sent to / received from the REPL is identical to Python's
  `model_dump(exclude_none=True, by_alias=True)`: camelCase keys (`allTactics`, `proofState`,
  `pickleTo`, `setOptions`, `pos`, `endPos`, ...), optional fields omitted when unset.
* **Request augmentation** identical: `Command`/`FileCommand` get `incrementality: true` when
  incremental optimization is enabled and `["Elab","async"], true` appended to `setOptions` when
  parallel elaboration is enabled (both default on).
* **Protocol framing** identical: write `json + "\n\n"`, read until accumulated output ends with
  `"\n\n"`; empty output → "closed unexpectedly"; a response whose only key is `message` (or `{}`
  for the auto server) is a `LeanError`.
* **Timeouts kill the REPL** (as in Python) because a half-answered request would desync the stream.
  Go: `context.Context` deadline/cancel. TS: `timeout` (ms) option and/or `AbortSignal`.
* **One request in flight per server** (Python `threading.Lock`). Go: `sync.Mutex`. TS: promise
  queue.
* **Session cache ids** are negative integers starting at -1, replaced by the per-server REPL id
  before sending; replay cache materializes lazily (recursively through `AutoServer.run`).
  Python's `WeakKeyDictionary[server → uuid]` becomes a random per-server `id` string.
* **Memory management** (`AutoLeanServer`): restart when system memory used ≥ `maxTotalMemory`
  or REPL process-tree RSS ≥ `maxProcessMemory * memoryHardLimitMb`; exponential backoff
  `2^attempt` seconds, `MemoryError` after `maxRestartAttempts`.
* **Hard memory limit** (`memory_hard_limit_mb`, Linux only): Python uses `preexec_fn` +
  `RLIMIT_AS`; neither Go nor Node have preexec, so the REPL is launched via
  `/bin/sh -c 'ulimit -v <kb>; exec "$@"'` on Linux. Same effect.
* **Process group kill**: REPL started in its own process group (`start_new_session`); kill sends
  SIGTERM to the group, then SIGKILL after 1 s. Windows: `taskkill /T /F`.
* **Default cache dir**: Python uses `<site-packages>/lean_interact/cache`. Ports use
  `$LEAN_INTERACT_CACHE_DIR` if set, else `<user cache dir>/lean_interact`
  (`~/Library/Caches` on macOS, `$XDG_CACHE_HOME` or `~/.cache` on Linux, `%LocalAppData%` on Windows).
* **Version ordering** of `<rev>_lean-toolchain-vX.Y.Z[-rcN]` tags: numeric components, a
  pre-release sorts before its release (PEP 440 semantics for the tags that actually exist).
* **String utils** keep Python semantics; indices are byte offsets in Go and UTF-16 offsets in TS
  (each consistent with that language's slicing). One deliberate fix: an unterminated `--`
  comment at end of input extends to end of string (Python's `str.find` returns -1 there, which
  makes `remove_lean_comments` keep the last character).
* **Upstream quirks kept for parity** (both ports): `InfoTree.dfs_walk` visits siblings
  last-to-first (`deque.extendleft`); `extract_last_theorem` returns the last `example` match if
  any, else last `lemma`, else last `theorem` (keyword order, not text position); the
  `\btheorem|lemma|example\s` keyword regex keeps Python's alternation precedence.
* **Upstream bugs fixed** (both ports): `PickleSessionCache.reload` no longer sends a stale
  proof-state id as `env`; request augmentation is idempotent (no duplicate `Elab.async` on
  replay); `git fetch` includes tags so new `_lean-toolchain-` tags are found after a pull; a
  failed REPL clone removes its half-created directory.
* **Replay of raw-id dependencies** (both ports, found by real-REPL testing): upstream's replay
  cache stores the request verbatim, so a cached `ProofStep` on a sorry's raw `proofState` (or a
  `Command` on a raw `env` of another cached state) cannot be replayed after a restart. The
  ports record the producing cached state (or sorry index) at `add` time and remap on replay.
  The pool additionally returns sorry proof states as session ids (via derived cache entries),
  because raw ids are local to one worker.
* **Verification**: unit + fake-REPL suites in both ports, plus `LEAN_INTERACT_E2E=1` suites run
  against a real REPL (`v1.3.19`, Lean `v4.36.0-rc1`, macOS arm64). Both ports can share one
  cache directory.

## Language-specific API shape

**Go**: typed `Run*` helpers return `(*CommandResponse, error)` / `(*ProofStepResponse, error)`;
a REPL-level error is returned as `*LeanError` (use `errors.As`). Generic `Run(ctx, Request)`
returns `Response` interface. Options that default to true in Python are exposed inverted
(`DisableIncrementalOptimization`, `SkipBuildREPL`, `NoAutoBuild`) so the zero value matches
Python defaults. Iteration over InfoTrees uses `iter.Seq`.

**TypeScript**: mirrors the Python API (`run()` resolves to `CommandResponse | LeanError`,
overloaded by request type). Responses are classes built from the raw JSON (unknown keys kept,
like pydantic `extra="allow"`); field names are the wire names. Request "classes" are plain
object types plus tagged constructors (`command({...})`, `proofStep({...})`) so the server knows
the response kind. Async factories (`await LeanREPLConfig.create()`) replace side-effecting
constructors; `Symbol.asyncDispose` supports `await using`.
