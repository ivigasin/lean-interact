<!-- Draft for https://github.com/augustepoiroux/LeanInteract/issues — not posted. -->

# Session cache: cached proof states get the wrong id, can't be replayed after restart, and pool sorry proof states are worker-local

**Environment:** lean-interact 0.11.6 (commit `768e18e`), REPL `v1.3.19`, Lean `v4.36.0-rc1`,
Python 3.14, macOS arm64.

Three related problems with proof states in the session cache. All three reproduce with the
script below. They don't affect cached *environments*, which is what the existing tests
(`test_replay_session_cache_*`) cover.

## Repro

```python
from lean_interact import AutoLeanServer, Command, LeanREPLConfig, LeanServerPool, ProofStep

config = LeanREPLConfig()
code = "theorem t (n : Nat) : n + 0 = n := sorry"

# 1 + 2: AutoLeanServer with the default ReplaySessionCache
server = AutoLeanServer(config)
thm = server.run(Command(cmd=code), add_to_session_cache=True)
step = server.run(ProofStep(proof_state=thm.sorries[0].proof_state, tactic="skip"),
                  add_to_session_cache=True)
print(step.proof_state, step.model_extra)        # 1 {'proofState': -2}   <- bug 1
print(server.run(ProofStep(proof_state=-2, tactic="simp")).proof_status)  # Completed
server.restart()
server.run(ProofStep(proof_state=-2, tactic="simp"))                       # <- bug 2
# ValueError: Could not replay the cached state. The Lean server returned an error: Unknown proof state.

# 3: LeanServerPool
with LeanServerPool(config, num_workers=2) as pool:
    thm = pool.run(Command(cmd=code))
    print(thm.env, thm.sorries[0].proof_state)    # -1 0
    for r in pool.run_batch([ProofStep(proof_state=thm.sorries[0].proof_state, tactic="simp")] * 4):
        print(r)                                  # <- bug 3
# ProofStepResponse(... proof_status='Completed')
# LeanError(message='Unknown proof state.')   x3
```

## 1. `add_to_session_cache=True` on a proof step returns the raw REPL id

**Expected:** `step.proof_state == -2`, the session id, as `env` is for commands.
**Actual:** `step.proof_state == 1`, the raw REPL id. The session id ends up in an extra
attribute, `step.model_extra == {'proofState': -2}`.

The cause is in `AutoLeanServer.run`:

```python
response = response.model_copy(update={"proofState": new_proof_state_id})
```

`model_copy(update=...)` takes field names, not aliases. `{"env": ...}` works for commands only
because that field name and its alias are the same. Fix:

```python
response = response.model_copy(update={"proof_state": new_proof_state_id})
```

Because of this, users never see the session id of a cached proof state. After a restart (or on
another server) they reuse the raw id, which points to nothing or to an unrelated state.

## 2. A cached proof step started from a sorry can't be replayed after a restart

**Expected:** after `restart()`, the cached proof state `-2` is rebuilt lazily: replay the
theorem (`-1`), then the `skip` step on the theorem's new sorry proof state.
**Actual:** `ValueError: ... Unknown proof state.`

`ReplaySessionCache.add` stores the request verbatim. The cached step's request is
`ProofStep(proof_state=0, ...)`, and `0` is a raw id local to the REPL process that just died.
On replay it is sent unchanged to the fresh REPL. Only negative ids are translated, and nothing
re-creates proof state `0`. The same applies to any cached request whose `env` / `proofState` is
a raw id produced by another cached state.

With `ReplaySessionCache(lazy=False)` it usually works by coincidence: replaying everything in
order happens to recreate the same raw ids. It breaks once the numbering differs, for example if
uncached commands ran before the cached ones.

**Suggested fix:** in `ReplaySessionCache.add`, if the request starts from a non-negative id,
look for the cached state that produced it on this server and point the stored request at that
state instead:

- an env or proof state whose `repl_ids[server_key]` equals the raw id: store its session id
  (replay then goes through the existing negative-id translation);
- a sorry of a cached `CommandResponse`: remember `(parent_session_id, sorry_index)`. This needs
  the sorry proof states stored per server for each cached command, both on `add` and after each
  replay. On replay, materialize the parent, then substitute
  `parent.sorries[server_key][sorry_index]`.

If nothing matches, keep the current behaviour.

## 3. `LeanServerPool` returns sorry proof states that only exist on one worker

**Expected:** a proof state taken from `pool.run(...)` can be used in any later pool request,
as `env` can.
**Actual:** `thm.env` is a session id (`-1`), but `thm.sorries[i].proof_state` is a raw id from
whichever worker ran the command. `_try_acquire_worker` only uses affinity for negative ids, so
follow-up `ProofStep`s go to any worker. There they either fail (`Unknown proof state`, 3 of 4
above) or, worse, silently run on an unrelated proof state with the same number.

**Suggested fix:** in `LeanServerPool.run`, after a `CommandResponse` is added to the shared
cache, register each sorry proof state as a cache entry derived from that environment (sorry `i`
of session state `env`, materialized by replaying the parent as in fix 2). Then replace
`response.sorries[i].proof_state` with the new negative ids, so they replay on any worker like
`env`.

---

Found while porting LeanInteract to Go and TypeScript. Both ports include these fixes, with
regression tests that fail without them, and pass the scenario above against a real REPL. Happy
to open a PR with the Python equivalent if that's useful.
