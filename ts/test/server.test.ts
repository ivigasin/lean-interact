// End-to-end tests against the fake REPL in test/fixtures/fake_repl.mjs.
import assert from "node:assert/strict";
import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, test } from "node:test";

import {
  AutoLeanServer,
  command,
  CommandResponse,
  fileCommand,
  LeanError,
  LeanMemoryError,
  LeanServer,
  LeanServerClosedError,
  LeanServerPool,
  LeanTimeoutError,
  PickleSessionCache,
  proofStep,
  ProofStepResponse,
  ReplaySessionCache,
  type LeanREPLConfig,
} from "../src/index.js";
import { fakeConfig } from "./helpers.js";

function ok<T>(res: T | LeanError): T {
  if (res instanceof LeanError) assert.fail(`unexpected LeanError: ${res.message}`);
  return res;
}

function echo(res: object): Record<string, unknown> {
  return (res as { echo: Record<string, unknown> }).echo;
}

describe("LeanREPLConfig (local REPL)", () => {
  test("infers the Lean version and directories", async () => {
    const { config, dir } = await fakeConfig();
    assert.equal(config.leanVersion, "v4.19.0");
    assert.equal(config.cacheReplDir, join(dir, "repl"));
    assert.equal(config.workingDir, join(dir, "repl"));
    assert.ok(config.isSetup());
    assert.deepEqual(await config.getAvailableLeanVersions(), ["v4.19.0"]);
  });

  test("rejects leanVersion together with project", async () => {
    const { config } = await fakeConfig();
    await assert.rejects(
      fakeConfig({ leanVersion: "v4.19.0", project: { getLeanVersion: () => "v4.19.0", directory: "/x" } as never }),
      /leanVersion should only be set/,
    );
    void config;
  });
});

describe("LeanServer", () => {
  test("runs commands, augments requests, parses responses", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    assert.ok(server.isAlive());

    const r1 = ok(await server.run(command({ cmd: "def x := 42" })));
    assert.ok(r1 instanceof CommandResponse);
    assert.equal(r1.env, 0);
    assert.deepEqual(r1.messages, []);
    assert.equal(echo(r1).incrementality, true);
    assert.deepEqual(echo(r1).setOptions, [[["Elab", "async"], true]]);

    const r2 = ok(await server.run({ cmd: "check x", env: r1.env }));
    assert.equal(r2.env, 1);
    assert.ok(!r2.hasErrors());

    const r3 = ok(await server.run(command({ cmd: "theorem zero_eq_zero : 0 = 0 := sorry" })));
    assert.equal(r3.sorries[0].proofState, 0);
    assert.equal(r3.sorries[0].goal, "⊢ 0 = 0");
    assert.ok(r3.leanCodeIsValid());
    assert.ok(!r3.leanCodeIsValid({ allowSorry: false }));

    const step = ok(await server.run(proofStep({ proofState: 0, tactic: "rfl" })));
    assert.ok(step instanceof ProofStepResponse);
    assert.equal(step.proofStatus, "Completed");
    assert.deepEqual(step.goals, []);

    const err = await server.run(command({ cmd: "def y := 1", env: 99 }));
    assert.ok(err instanceof LeanError);
    assert.equal(err.message, "Unknown environment.");
  });

  test("does not augment when disabled; runDict is raw", async () => {
    const { config } = await fakeConfig({ enableIncrementalOptimization: false, enableParallelElaboration: false });
    await using server = new LeanServer(config);
    const r = ok(await server.run(command({ cmd: "def a := 1", setOptions: [[["pp", "all"], true]] })));
    assert.equal(echo(r).incrementality, undefined);
    assert.deepEqual(echo(r).setOptions, [[["pp", "all"], true]]);
    const raw = await server.runDict({ cmd: "def b := 2" });
    assert.equal(raw.env, 1);
  });

  test("FileCommand on a missing file returns LeanError", async () => {
    const { config, dir } = await fakeConfig();
    await using server = new LeanServer(config);
    const out = await server.run(fileCommand({ path: "nonexistent_file.lean" }));
    assert.ok(out instanceof LeanError);
    assert.match(out.message.toLowerCase(), /no such file or directory/);
    assert.match(out.message, /file: nonexistent_file.lean/);
    const file = join(dir, "repl", "a.lean");
    writeFileSync(file, "def fromFile := 1");
    assert.ok(ok(await server.run(fileCommand({ path: file }))) instanceof CommandResponse);
  });

  test("concurrent requests are serialized", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    const results = await Promise.all(Array.from({ length: 20 }, (_, i) => server.run(command({ cmd: `def v${i} := ${i}` }))));
    assert.deepEqual(
      results.map((r) => ok(r).env).sort((a, b) => a - b),
      Array.from({ length: 20 }, (_, i) => i),
    );
    for (let i = 0; i < 20; i++) assert.equal(echo(ok(results[i])).cmd, `def v${i} := ${i}`);
  });

  test("long commands go through", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    const cmd = "def big := " + "1 + ".repeat(50_000) + "1";
    assert.equal(echo(ok(await server.run(command({ cmd })))).cmd, cmd);
  });

  test("timeout kills the server", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    const start = Date.now();
    await assert.rejects(server.run(command({ cmd: "sleep 5000" }), { timeout: 300 }), LeanTimeoutError);
    assert.ok(Date.now() - start < 3000);
    assert.ok(!server.isAlive());
    await assert.rejects(server.run(command({ cmd: "def x := 1" })), { name: "ChildProcessError" });
  });

  test("AbortSignal kills the server", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    const ac = new AbortController();
    setTimeout(() => ac.abort(new Error("stop")), 200);
    await assert.rejects(server.run(command({ cmd: "sleep 5000" }), { signal: ac.signal }), /stop/);
    assert.ok(!server.isAlive());
  });

  test("crash surfaces as ConnectionAbortedError", async () => {
    const { config } = await fakeConfig();
    await using server = new LeanServer(config);
    await assert.rejects(server.run(command({ cmd: "crash" })), LeanServerClosedError);
    assert.ok(!server.isAlive());
    await server.restart();
    assert.equal(ok(await server.run(command({ cmd: "def x := 1" }))).env, 0);
  });

  test("restart gives a fresh process; kill is idempotent", async () => {
    const { config } = await fakeConfig();
    const server = new LeanServer(config);
    const pid1 = (ok(await server.run(command({ cmd: "def x := 1" }))) as { pid?: number }).pid;
    assert.ok((await server.getMemoryUsage()) > 0);
    await server.restart();
    const pid2 = (ok(await server.run(command({ cmd: "def x := 1" }))) as { pid?: number }).pid;
    assert.notEqual(pid1, pid2);
    await server.kill();
    await server.kill();
    assert.ok(!server.isAlive());
    assert.equal(await server.getMemoryUsage(), 0);
  });
});

describe("AutoLeanServer", () => {
  test("session cache ids and replay after restart", async () => {
    const { config } = await fakeConfig();
    await using server = new AutoLeanServer(config);
    const r1 = ok(await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    assert.equal(r1.env, -1);
    const r2 = ok(await server.run(command({ cmd: "check x", env: -1 })));
    assert.equal(r2.env, 1);
    assert.equal(echo(r2).env, 0);

    await server.restart();
    // Lazy replay: env -1 is rebuilt on demand in the new process.
    const r3 = ok(await server.run(command({ cmd: "check x", env: -1 })));
    assert.ok(!r3.hasErrors(), JSON.stringify(r3.messages));
    assert.equal(echo(r3).env, 0);
  });

  test("proof states in the session cache, including chained replay", async () => {
    const { config } = await fakeConfig();
    await using server = new AutoLeanServer(config);
    const thm = ok(await server.run(command({ cmd: "theorem t : (x : Nat) -> x = x := sorry" }), { addToSessionCache: true }));
    assert.equal(thm.env, -1);
    const step = ok(await server.run(proofStep({ proofState: 0, tactic: "intro x" }), { addToSessionCache: true }));
    assert.equal(step.proofState, -2);
    const done = ok(await server.run(proofStep({ proofState: -2, tactic: "rfl" })));
    assert.equal(done.proofStatus, "Completed");
  });

  test("recovers after a timeout and a crash", async () => {
    const { config } = await fakeConfig();
    await using server = new AutoLeanServer(config);
    ok(await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    await assert.rejects(server.run(command({ cmd: "sleep 5000" }), { timeout: 200 }), LeanTimeoutError);
    assert.ok(!server.isAlive());
    const r = ok(await server.run(command({ cmd: "check x", env: -1 })));
    assert.ok(!r.hasErrors());
    await assert.rejects(server.run(command({ cmd: "crash" })), LeanServerClosedError);
    assert.ok(!ok(await server.run(command({ cmd: "check x", env: -1 }))).hasErrors());
  });

  test("runDict is not available", async () => {
    const { config } = await fakeConfig();
    await using server = new AutoLeanServer(config);
    await assert.rejects(server.runDict({ cmd: "x" }), /not available with automated memory management/);
  });

  test("memory pressure: backoff then MemoryError", async () => {
    const { config } = await fakeConfig();
    class FastBackoff extends AutoLeanServer {
      delays: number[] = [];
      protected override backoffDelayMs(attempt: number): number {
        this.delays.push(attempt);
        return 1;
      }
    }
    await using server = new FastBackoff(config, { maxTotalMemory: 0, maxRestartAttempts: 2 });
    await assert.rejects(server.run(command({ cmd: "def x := 1" })), LeanMemoryError);
    assert.deepEqual(server.delays, [0, 1]);
    assert.ok(!server.isAlive());
  });

  test("clearSessionCache and removeFromSessionCache", async () => {
    const { config } = await fakeConfig();
    await using server = new AutoLeanServer(config);
    ok(await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    ok(await server.run(command({ cmd: "def y := 1" }), { addToSessionCache: true }));
    await server.removeFromSessionCache(-1);
    assert.deepEqual(server.sessionCache.keys(), [-2]);
    await server.clearSessionCache(true);
    assert.ok(server.sessionCache.isEmpty());
    assert.ok(server.isAlive());
  });

  test("shared replay cache across servers", async () => {
    const { config } = await fakeConfig();
    const cache = new ReplaySessionCache();
    await using a = new AutoLeanServer(config, { sessionCache: cache });
    await using b = new AutoLeanServer(config, { sessionCache: cache });
    ok(await a.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    assert.ok(cache.isMaterialized(-1, a));
    assert.ok(!cache.isMaterialized(-1, b));
    // b has never seen env -1: it is replayed there; concurrent uses share one replay.
    const [r1, r2] = await Promise.all([b.run(command({ cmd: "check x", env: -1 })), b.run(command({ cmd: "check x", env: -1 }))]);
    assert.ok(!ok(r1).hasErrors() && !ok(r2).hasErrors());
    assert.ok(cache.isMaterialized(-1, b));
    assert.equal(cache.get(-1).replIds.get(b.id), 0);
  });

  test("eager replay cache rematerializes on restart", async () => {
    const { config } = await fakeConfig();
    const cache = new ReplaySessionCache({ lazy: false });
    await using server = new AutoLeanServer(config, { sessionCache: cache });
    ok(await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    ok(await server.run(command({ cmd: "def y := 1", env: -1 }), { addToSessionCache: true }));
    await server.restart();
    assert.ok(cache.isMaterialized(-1, server));
    assert.ok(cache.isMaterialized(-2, server));
  });

  test("pickle session cache survives restarts", async () => {
    const { config, dir } = await fakeConfig();
    const cache = new PickleSessionCache(join(dir, "pickles"));
    await using server = new AutoLeanServer(config, { sessionCache: cache });
    ok(await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
    const file = cache.get(-1).pickleFile;
    assert.ok(existsSync(file));
    await server.restart();
    assert.ok(!ok(await server.run(command({ cmd: "check x", env: -1 }))).hasErrors());
    await cache.clear();
    assert.ok(!existsSync(file));
  });
});

describe("LeanServerPool", () => {
  async function pool(config: LeanREPLConfig) {
    return new LeanServerPool(config, { numWorkers: 3 });
  }

  test("runBatch preserves order and reports per-item errors", async () => {
    const { config } = await fakeConfig();
    await using p = await pool(config);
    let progress = 0;
    const reqs = [
      command({ cmd: "def a := 1" }),
      command({ cmd: "sleep 100" }),
      command({ cmd: "def b := 1", env: 1234 }),
      command({ cmd: "sleep 20000" }),
      command({ cmd: "def c := 1" }),
      command({ cmd: "def d := 1" }),
    ];
    const results = await p.runBatch(reqs, { timeoutPerCmd: 4000, onProgress: (d) => (progress = d) });
    assert.equal(results.length, 6);
    assert.equal(progress, 6);
    for (const i of [0, 1, 4, 5]) {
      assert.ok(results[i] instanceof CommandResponse, `${i}: ${String(results[i])}`);
      assert.equal(echo(results[i] as CommandResponse).cmd, (reqs[i] as { cmd: string }).cmd);
      assert.ok((results[i] as CommandResponse).env < 0);
    }
    assert.ok(results[2] instanceof LeanError);
    assert.ok(results[3] instanceof LeanTimeoutError);
  });

  test("session ids work on any worker", async () => {
    const { config } = await fakeConfig();
    await using p = await pool(config);
    const base = ok(await p.run(command({ cmd: "def x := 1" })));
    const results = await p.runBatch(Array.from({ length: 9 }, () => command({ cmd: "check x", env: base.env })));
    for (const r of results) assert.ok(r instanceof CommandResponse && !r.hasErrors(), String(r));
  });

  test("sorry proof states come back as session ids usable on any worker", async () => {
    const { config } = await fakeConfig();
    await using p = await pool(config);
    const thm = ok(await p.run(command({ cmd: "theorem t : 0 = 0 := sorry" })));
    const ps = thm.sorries[0]!.proofState!;
    assert.ok(ps < 0, `expected a session id, got ${ps}`);
    const results = await p.runBatch(Array.from({ length: 9 }, () => proofStep({ proofState: ps, tactic: "rfl" })));
    for (const r of results) assert.ok(r instanceof ProofStepResponse && r.proofStatus === "Completed", String(r));
  });
});

// A cached proof step that starts from a sorry of a cached command stores a raw, server-local
// proof state id. It must still replay after a restart (upstream LeanInteract fails here).
describe("ReplaySessionCache with sorry proof states", () => {
  for (const lazy of [true, false]) {
    test(`replays after restart (lazy=${lazy})`, async () => {
      const { config } = await fakeConfig();
      await using srv = new AutoLeanServer(config, { sessionCache: new ReplaySessionCache({ lazy }) });
      // Uncached sorries first, so the cached proof state ids are not the ones a replay on a
      // fresh REPL produces.
      for (let i = 0; i < 3; i++) ok(await srv.run(command({ cmd: "theorem u : 0 = 0 := sorry" })));
      const base = ok(await srv.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }));
      const thm = ok(await srv.run(command({ cmd: "theorem t : 0 = 0 := sorry", env: base.env }), { addToSessionCache: true }));
      const step = ok(
        await srv.run(proofStep({ proofState: thm.sorries[0]!.proofState!, tactic: "intro x" }), { addToSessionCache: true }),
      );
      await srv.restart();
      const done = ok(await srv.run(proofStep({ proofState: step.proofState, tactic: "rfl" })));
      assert.equal(done.proofStatus, "Completed");
    });
  }
});
