# lean-interact (TypeScript)

A TypeScript port of [LeanInteract](https://github.com/augustepoiroux/LeanInteract): talk to the
[Lean 4](https://lean-lang.org) theorem prover from Node.js through the
[Lean REPL](https://github.com/leanprover-community/repl).

- Run Lean code, files and tactics; get messages, sorries, tactics, declarations and InfoTrees back
- Works with any Lean version from v4.8.0-rc1 on; the matching REPL is fetched and built for you
- Projects: an existing directory, a git repository, or a temporary project (custom lakefile or
  dependencies such as Mathlib)
- `AutoLeanServer`: restarts on memory pressure, timeouts and crashes, and keeps your session states
- `LeanServerPool`: several REPLs sharing one session cache
- Pickling of environments and proof states
- No runtime dependencies. Node.js >= 20, ESM.

## Install

```bash
npm install lean-interact
```

You also need `git` and Lean's version manager [elan](https://github.com/leanprover/elan)
(`lake` on your `PATH`). To install elan:

```bash
npx lean-interact install-lean
```

## Quick start

```ts
import { LeanREPLConfig, LeanServer, command, proofStep, LeanError } from "lean-interact";

const config = await LeanREPLConfig.create({ verbose: true }); // downloads & builds the REPL the first time
await using server = new LeanServer(config);

const res = await server.run(command({ cmd: "theorem ex (n : Nat) : n = 5 → n = 5 := id" }));
if (res instanceof LeanError) throw new Error(res.message);
console.log(res.env, res.messages);
```

### Environments

Each command returns an `env` you can continue from:

```ts
const r1 = await server.run(command({ cmd: "def x := 1" }));
if (r1 instanceof LeanError) throw new Error(r1.message);
const r2 = await server.run(command({ cmd: "#eval x + 1", env: r1.env }));
```

### Tactic mode

```ts
const thm = await server.run(command({ cmd: "theorem t (x : Nat) : x = x := sorry" }));
if (thm instanceof LeanError) throw new Error(thm.message);
const ps = thm.sorries[0].proofState!;
const step = await server.run(proofStep({ proofState: ps, tactic: "rfl" }));
if (!(step instanceof LeanError)) console.log(step.proofStatus); // "Completed"
```

### Options

```ts
await server.run(
  command({
    cmd: "theorem t : 0 = 0 := by rfl",
    allTactics: true,
    declarations: true,
    rootGoals: true,
    infotree: "original", // "full" | "tactics" | "original" | "substantive"
    setOptions: [[["pp", "all"], true]],
  }),
);
```

Responses expose helpers like the Python ones: `getErrors()`, `getWarnings()`, `hasErrors()`,
`leanCodeIsValid({ startPos, endPos, allowSorry })`. InfoTrees are `InfoTree` instances with the
generators `dfsWalk()`, `leaves()`, `commands()`, `variables()`, `theorems()`, `docs()`,
`namespaces()`, plus `ppUpTo(pos)` and `theoremForSorry(sorry)`.

### Files, pickling, raw requests

```ts
import { fileCommand, pickleEnvironment, unpickleEnvironment } from "lean-interact";

await server.run(fileCommand({ path: "MyFile.lean" }));
await server.run(pickleEnvironment({ env: 0, pickleTo: "env.olean" }));
await server.run(unpickleEnvironment({ unpickleEnvFrom: "env.olean" }));

// Bypass parsing for custom REPLs:
const raw = await server.runDict({ cmd: "#eval 1" });
```

Plain objects work too (`server.run({ cmd: "#eval 1" })`); the request kind is inferred from its
keys. The constructor helpers additionally validate required fields.

### Timeouts and cancellation

```ts
await server.run(command({ cmd: "..." }), { timeout: 10_000 }); // ms
await server.run(command({ cmd: "..." }), { signal: AbortSignal.timeout(10_000) });
```

On timeout or abort the REPL process is killed (a half-answered request cannot be recovered). A
`LeanServer` then needs `restart()`; an `AutoLeanServer` restarts by itself on the next request.

### AutoLeanServer and the session cache

```ts
import { AutoLeanServer } from "lean-interact";

await using server = new AutoLeanServer(config, { maxTotalMemory: 0.8, maxRestartAttempts: 5 });
const base = await server.run(command({ cmd: "import Lean\ndef x := 1" }), { addToSessionCache: true });
// base.env === -1: a session id that stays valid across restarts
await server.run(command({ cmd: "#eval x", env: -1 }));
```

`ReplaySessionCache` (default) replays the original command when needed (lazily, or eagerly with
`new ReplaySessionCache({ lazy: false })`). `PickleSessionCache(dir)` stores states with the REPL
pickling feature instead.

### Projects

```ts
import { LocalProject, GitProject, TemporaryProject, TempRequireProject, LeanRequire } from "lean-interact";

const local = await LocalProject.create({ directory: "/path/to/project" });
const git = await GitProject.create({ url: "https://github.com/user/lean-project", rev: "main" });
const mathlib = await TempRequireProject.create({ leanVersion: "v4.19.0", require: "mathlib" });
const custom = await TempRequireProject.create({
  leanVersion: "v4.19.0",
  require: [new LeanRequire("mathlib", "https://github.com/leanprover-community/mathlib4.git", "v4.19.0")],
});
const temp = await TemporaryProject.create({ leanVersion: "v4.19.0", content: "import Lake\n...", lakefileType: "lean" });

const config = await LeanREPLConfig.create({ project: mathlib });
```

### Pool

```ts
import { LeanServerPool } from "lean-interact";

await using pool = new LeanServerPool(config, { numWorkers: 4 });
const results = await pool.runBatch(
  theorems.map((cmd) => command({ cmd })),
  { timeoutPerCmd: 60_000, onProgress: (done, total) => console.log(`${done}/${total}`) },
);
// each result: CommandResponse | ProofStepResponse | LeanError | Error
```

### Utilities

`cleanTheoremString`, `cleanLastTheoremString`, `extractLastTheorem`, `removeLeanComments`,
`leanCommentsRanges`, `splitImplementation`, `splitConclusion`, `compressNewlines`, `indentCode`,
`parseLeanVersion`, `getProjectLeanVersion`, `compareLeanVersions`, `clearCache`.

### CLI

```bash
npx lean-interact install-lean
npx lean-interact clear-cache [--cache-dir <dir>]
```

### Logging

Logs go to stderr at level `info` by default. Change with `LEAN_INTERACT_LOG_LEVEL=debug` (or
`setLogLevel("warn")`), or route them with `setLogger(myLogger)`.

## Differences from the Python library

| Python | TypeScript |
|---|---|
| `LeanREPLConfig(...)` / `GitProject(...)` constructors do the setup | `await LeanREPLConfig.create({...})`, `await GitProject.create({...})` |
| snake_case fields (`proof_state`, `start_pos`, `all_tactics`) | REPL wire names (`proofState`, `pos`, `allTactics`) |
| `run` / `async_run` | `run` returns a promise (`asyncRun` is an alias) |
| `timeout` in seconds | `timeout` in milliseconds, plus `signal: AbortSignal` |
| `with LeanServer(...)` | `await using server = new LeanServer(...)` or `await server.kill()` |
| `run_batch(show_progress=True)` | `runBatch(reqs, { onProgress })` |
| Cache in the package directory | `$LEAN_INTERACT_CACHE_DIR` or the user cache dir (`~/Library/Caches/lean_interact`, `~/.cache/lean_interact`, `%LocalAppData%\lean_interact`) |
| `memory_hard_limit_mb` via `preexec_fn` | same limit via `sh -c 'ulimit -v …'` (Linux only) |
| Concurrent use of the same uncached state on one server raises | concurrent requests share a single replay |
| `["Elab","async"]` appended again on every replay | appended once |
| `PickleSessionCache.reload` passes a stale proof-state id as `env` | no `env` is sent |
| A cached request that starts from a raw REPL id (e.g. a `ProofStep` on a sorry's `proofState`) cannot be replayed after a restart | the dependency is recorded and remapped on replay |
| `LeanServerPool` returns sorry `proofState`s as raw ids of one worker, so follow-up proof steps may hit another worker | sorry proof states are returned as session ids, valid on every worker |

Behavior otherwise follows the Python library, including some quirks kept for parity:
`dfsWalk()` visits siblings last-to-first, `extractLastTheorem` returns the last `example` match
if there is one, else the last `lemma`, else the last `theorem` (not necessarily the textually
last), and `cleanTheoremString` uses the same keyword regex. One intentional fix: a trailing `--`
comment with no newline after it is removed entirely (Python keeps its last character). String
utilities return UTF-16 indices (JavaScript string offsets).

## Development

```bash
npm install
npm run build
npm test   # unit tests + end-to-end tests against a fake REPL (no Lean needed)
npm run test:e2e   # against a real Lean REPL: needs `lake` on PATH and network for the first build
```

`test:e2e` honours `LEAN_INTERACT_CACHE_DIR` and `LEAN_INTERACT_E2E_LEAN_VERSION` (default: the
latest version the REPL supports).
