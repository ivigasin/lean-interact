# LeanInteract for Go

A Go port of [LeanInteract](https://github.com/augustepoiroux/LeanInteract): drive the
[Lean 4 REPL](https://github.com/leanprover-community/repl) from Go — run commands, step
through tactics, extract declarations and InfoTrees, pickle states, and run many REPLs in
parallel. No third-party dependencies.

```
go get github.com/ivigasin/lean-interact/go
```

Requirements: Go ≥ 1.23, `git`, and Lean's `lake` on `PATH` (install it with
`go run github.com/ivigasin/lean-interact/go/cmd/lean-interact install-lean`, or see
the [Lean docs](https://leanprover-community.github.io/get_started.html)).

## Basic usage

```go
ctx := context.Background()

// Clones and builds the REPL for the requested Lean version (cached afterwards).
cfg, err := leaninteract.NewConfig(ctx, leaninteract.ConfigOptions{LeanVersion: "v4.19.0", Verbose: true})
if err != nil { log.Fatal(err) }

srv, err := leaninteract.NewServer(cfg)
if err != nil { log.Fatal(err) }
defer srv.Kill()

resp, err := srv.RunCommand(ctx, leaninteract.Command{Cmd: "theorem ex (n : Nat) : n = 5 → n = 5 := id"})
if le, ok := leaninteract.AsLeanError(err); ok {
	log.Fatalf("REPL error: %s", le.Message)
} else if err != nil {
	log.Fatal(err) // timeout, crash, ...
}
fmt.Println(resp.Env, resp.LeanCodeIsValid(nil, nil, false))

// Continue in the environment produced above.
resp2, _ := srv.RunCommand(ctx, leaninteract.Command{Cmd: "example : ex 5 rfl = rfl := rfl", Env: leaninteract.Ptr(resp.Env)})
```

## Tactic mode

```go
r, _ := srv.RunCommand(ctx, leaninteract.Command{Cmd: "theorem t (x : Nat) : x = x := sorry"})
ps := *r.Sorries[0].ProofState

step, _ := srv.RunProofStep(ctx, leaninteract.ProofStep{ProofState: ps, Tactic: "rfl"})
fmt.Println(step.ProofStatus, step.Goals) // Completed []
```

## Timeouts

Every call takes a `context.Context`. If it is done before the REPL answers, the REPL is
killed (as in Python, the stream cannot be resynchronized). `AutoServer` restarts it on the
next request.

```go
ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
_, err := srv.RunCommand(ctx, leaninteract.Command{Cmd: code})
if errors.Is(err, leaninteract.ErrTimeout) { ... }
```

## Auto-restarting server and session cache

```go
srv, _ := leaninteract.NewAutoServer(cfg, leaninteract.AutoServerOptions{})
defer srv.Kill()

// Cached states get negative ids that survive restarts (replayed lazily when needed).
base, _ := srv.RunCommand(ctx, leaninteract.Command{Cmd: "def x := 1"}, leaninteract.AddToSessionCache())
fmt.Println(base.Env) // -1

srv.Restart(ctx)
srv.RunCommand(ctx, leaninteract.Command{Cmd: "#eval x", Env: leaninteract.Ptr(base.Env)})
```

`AutoServer` also restarts the REPL (with exponential backoff) when system memory use
exceeds `MaxTotalMemory` (default 80 %), or when the REPL exceeds `MaxProcessMemory` of
`ConfigOptions.MemoryHardLimitMB`.

Caches: `NewReplaySessionCache(lazy)` (default, replays the creating request) and
`NewPickleSessionCache(dir)` (uses REPL pickling; less reliable upstream).

## Pools

```go
pool, _ := leaninteract.NewPool(cfg, leaninteract.PoolOptions{NumWorkers: 4})
defer pool.Close()

results := pool.RunBatch(ctx, []leaninteract.Request{
	leaninteract.Command{Cmd: "theorem a : 1 + 1 = 2 := rfl"},
	leaninteract.Command{Cmd: "theorem b : 2 + 2 = 4 := rfl"},
}, leaninteract.BatchOptions{TimeoutPerRequest: time.Minute})
for _, r := range results {
	if r.Err != nil { ... }
	fmt.Println(r.Response.(*leaninteract.CommandResponse).Env) // negative, usable on any worker
}
```

## Projects

```go
// Existing project
p, _ := leaninteract.NewLocalProject(ctx, leaninteract.LocalProjectOptions{Directory: "/path/to/project"})

// Git repository
p, _ := leaninteract.NewGitProject(ctx, leaninteract.GitProjectOptions{URL: "https://github.com/user/repo", Rev: "main"})

// Temporary project with Mathlib (cached by content hash)
p, _ := leaninteract.NewTempRequireProject(ctx, leaninteract.TempProjectOptions{LeanVersion: "v4.19.0"}, leaninteract.Mathlib)

// Temporary project with a custom lakefile ("lean" or "toml")
p, _ := leaninteract.NewTemporaryProject(ctx, leaninteract.TempProjectOptions{LeanVersion: "v4.19.0"}, lakefile, "lean")

cfg, _ := leaninteract.NewConfig(ctx, leaninteract.ConfigOptions{Project: p})
```

## Data extraction

```go
r, _ := srv.RunCommand(ctx, leaninteract.Command{
	Cmd:            code,
	CommandOptions: leaninteract.CommandOptions{Declarations: true, AllTactics: true, InfoTree: leaninteract.InfoTreeFull},
})
for _, d := range r.Declarations { fmt.Println(d.FullName, d.Kind, d.Signature.PP) }
for _, tree := range r.InfoTree {
	for thm := range tree.Theorems() { fmt.Println(thm.Node.Stx.Range) }
}
```

`RunDict` sends raw JSON for custom REPLs. String helpers (`CleanTheoremString`,
`RemoveLeanComments`, `SplitImplementation`, ...) are in `utils.go`.

## CLI

```
go run github.com/ivigasin/lean-interact/go/cmd/lean-interact install-lean
go run github.com/ivigasin/lean-interact/go/cmd/lean-interact clear-cache
```

## Differences from Python

* REPL errors are returned as `*LeanError` errors (use `AsLeanError` / `errors.As`), not as
  response values. `Run` returns a `Response` interface; `RunCommand` / `RunProofStep` are
  the typed variants.
* Timeouts are `context.Context` deadlines; there is no separate `async_run` (use goroutines).
* Options that default to `True` in Python are inverted so the zero value matches:
  `SkipBuildREPL`, `DisableIncrementalOptimization`, `DisableParallelElaboration`,
  `NoAutoBuild`. Build output is quiet unless `Verbose` is set (Python projects default to
  verbose).
* Default cache: `$LEAN_INTERACT_CACHE_DIR` or `<user cache dir>/lean_interact` instead of the
  Python package directory.
* `InfoNode` is one struct with the union of TacticNode / CommandNode / TermNode fields;
  `InfoTree.Kind` tells which apply. Iterators are `iter.Seq`. `DFSWalk` keeps Python's order
  (siblings last-to-first).
* Unknown JSON fields are dropped by the typed responses (pydantic keeps them); use `RunDict`
  on a `Server` if you need them.
* Memory hard limit uses `sh -c 'ulimit -v'` on Linux (Go has no `preexec_fn`). Process memory
  is read from `/proc` (Linux) or `ps` (other Unix); not implemented on Windows (reported as 0).
* String helper indices are byte offsets; `\b` and `\s` in the theorem regexes are ASCII
  (Python's are Unicode). An unterminated `--` comment now extends to end of input.
* `PickleSessionCache.Reload` unpickles proof states without passing a stale id as `env`.
* Requests are augmented idempotently (`Elab.async` is not appended twice on replay).
* A cached request that starts from a raw REPL id produced by another cached state (e.g. a
  `ProofStep` on a sorry's `ProofState`) is remapped on replay; upstream cannot replay it after
  a restart.
* `Pool` returns sorry proof states as session ids valid on every worker; upstream returns raw
  ids of one worker, so follow-up proof steps may silently target another worker.
* Windows file locks are marker files (Unix uses `flock`); a crashed process can leave a
  stale `*.lock.pid` file.

## Testing

```
go test ./...                                  # unit tests + fake-REPL end-to-end tests
LEAN_INTERACT_E2E=1 go test -run TestE2E ./... # against a real Lean REPL (needs lake on PATH)
```

The real-REPL tests honour `LEAN_INTERACT_CACHE_DIR` and `LEAN_INTERACT_E2E_LEAN_VERSION`
(default: the latest version the REPL supports).
