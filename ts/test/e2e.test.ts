// End-to-end tests against a real Lean REPL. They need `lake` on PATH and network access for the
// first REPL clone/build, so they only run with LEAN_INTERACT_E2E=1. Expectations mirror the
// upstream Python test suite.
import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { before, describe, test } from "node:test";

import {
  AutoLeanServer,
  command,
  CommandResponse,
  fileCommand,
  LeanError,
  LeanREPLConfig,
  LeanServer,
  LeanServerPool,
  LeanTimeoutError,
  pickleEnvironment,
  pickleProofState,
  PickleSessionCache,
  proofStep,
  ProofStepResponse,
  ReplaySessionCache,
  unpickleEnvironment,
  unpickleProofState,
  type BaseSessionCache,
} from "../src/index.js";

const enabled = process.env.LEAN_INTERACT_E2E === "1";
const MIN = 60_000;

function ok<T>(res: T | LeanError | Error): T {
  if (res instanceof LeanError) assert.fail(`unexpected LeanError: ${res.message}`);
  if (res instanceof Error) throw res;
  return res;
}

function cmdRes(res: unknown): CommandResponse {
  const r = ok(res);
  assert.ok(r instanceof CommandResponse, `expected CommandResponse, got ${String(r)}`);
  return r;
}

function stepRes(res: unknown): ProofStepResponse {
  const r = ok(res);
  assert.ok(r instanceof ProofStepResponse, `expected ProofStepResponse, got ${String(r)}`);
  return r;
}

function count(it: Iterable<unknown>): number {
  let n = 0;
  for (const _ of it) n++;
  return n;
}

describe("e2e: real Lean REPL", { skip: !enabled && "set LEAN_INTERACT_E2E=1" }, () => {
  let config: LeanREPLConfig;

  before(async () => {
    config = await LeanREPLConfig.create({ leanVersion: process.env.LEAN_INTERACT_E2E_LEAN_VERSION || undefined });
  }, { timeout: 30 * MIN });

  test("config", async () => {
    assert.match(config.leanVersion ?? "", /^v4/);
    const versions = await config.getAvailableLeanVersions();
    assert.ok(versions.length > 0);
    assert.equal(versions[versions.length - 1], config.leanVersion);
  });

  test("commands, sorries and proof steps", { timeout: 5 * MIN }, async () => {
    await using srv = new AutoLeanServer(config);
    let r = cmdRes(await srv.run(command({ cmd: "def x := 42" })));
    assert.equal(r.env, 0);
    assert.deepEqual(r.messages, []);

    r = cmdRes(await srv.run(command({ cmd: "theorem zero_eq_zero : 0 = 0 := sorry" }), { addToSessionCache: true }));
    assert.equal(r.env, -1);
    assert.equal(r.messages.length, 1);
    const [m] = r.messages;
    assert.equal(m!.severity, "warning");
    assert.deepEqual(m!.pos, { line: 1, column: 8 });
    assert.deepEqual(m!.endPos, { line: 1, column: 20 });
    assert.match(m!.data, /^declaration uses/);
    const [s] = r.sorries;
    assert.equal(s!.goal, "⊢ 0 = 0");
    assert.deepEqual(s!.pos, { line: 1, column: 32 });
    assert.deepEqual(s!.endPos, { line: 1, column: 37 });
    assert.ok(r.leanCodeIsValid());
    assert.ok(!r.leanCodeIsValid({ allowSorry: false }));

    const step = stepRes(await srv.run(proofStep({ proofState: s!.proofState!, tactic: "rfl" })));
    assert.equal(step.proofStatus, "Completed");
    assert.deepEqual(step.goals, []);

    r = cmdRes(await srv.run(command({ cmd: "theorem seq : (x : Nat) -> x = x := sorry" })));
    const s1 = stepRes(await srv.run(proofStep({ proofState: r.sorries[0]!.proofState!, tactic: "intro x" })));
    assert.equal(s1.goals.length, 1);
    const s2 = stepRes(await srv.run(proofStep({ proofState: s1.proofState, tactic: "rfl" })));
    assert.equal(s2.proofStatus, "Completed");

    r = cmdRes(await srv.run(command({ cmd: "example : 1 = 2 := rfl" })));
    assert.ok(r.hasErrors());
    assert.ok(r.getErrors().length > 0);
    assert.ok(!r.leanCodeIsValid());
  });

  test("nonexistent file returns LeanError", { timeout: 5 * MIN }, async () => {
    await using srv = new LeanServer(config);
    const res = await srv.run(fileCommand({ path: "nonexistent_file.lean" }));
    assert.ok(res instanceof LeanError);
    assert.match(res.message.toLowerCase(), /no such file or directory/);
    assert.match(res.message, /file: nonexistent_file\.lean/);
    assert.ok(srv.isAlive());
  });

  test("declarations", { timeout: 5 * MIN }, async () => {
    await using srv = new LeanServer(config);
    const r = cmdRes(
      await srv.run(command({ cmd: "variable (p : Prop)\ntheorem test (h : p) : 0 = 0 := by rfl", declarations: true })),
    );
    assert.equal(r.declarations.length, 1);
    const d = r.declarations[0]!;
    assert.equal(d.fullName, "test");
    assert.equal(d.kind, "theorem");
    assert.equal(d.signature.pp, "(h : p) : 0 = 0");
    assert.equal(d.type?.pp, "0 = 0");
    assert.deepEqual(d.binders?.map, [{ id: "h", type: "p", binderInfo: "default" }]);
    assert.equal(d.value?.pp, ":= by rfl");
    assert.deepEqual(d.scope.varDecls, ["variable (p : Prop)"]);
    assert.equal(d.modifiers.visibility, "regular");
    assert.equal(d.modifiers.recKind, "default");
    assert.deepEqual(d.range.start, { line: 2, column: 0 });
    assert.deepEqual(d.range.finish, { line: 2, column: 38 });
  });

  test("infotrees and tactics", { timeout: 5 * MIN }, async () => {
    await using srv = new LeanServer(config);
    for (const infotree of ["full", "tactics", "original", "substantive"]) {
      const r = cmdRes(await srv.run(command({ cmd: "theorem infotree_test : 0 = 0 := by rfl", infotree })));
      assert.ok(r.infotree !== null, `infotree=${infotree}`);
    }
    let r = cmdRes(await srv.run(command({ cmd: "theorem infotree_test : 0 = 0 := by rfl", infotree: "invalid" })));
    assert.equal(r.infotree, null);

    r = cmdRes(
      await srv.run(command({ cmd: "variable (p : Prop)\ntheorem infotree_test (h : p) : 0 = 0 := by rfl", infotree: "full" })),
    );
    assert.equal(count(r.infotree![0]!.variables()), 1);
    assert.equal(count(r.infotree![1]!.theorems()), 1);

    r = cmdRes(await srv.run(command({ cmd: "theorem infotree_test : 0 = 0 := by sorry", infotree: "full" })));
    assert.equal(r.sorries.length, 1);
    assert.equal(count(r.infotree![0]!.commands()), 1);
    assert.equal(count(r.infotree![0]!.theorems()), 1);
    assert.ok(r.infotree![0]!.theoremForSorry(r.sorries[0]!) !== null);

    r = cmdRes(await srv.run(command({ cmd: "theorem t2 (n : Nat) : n + 0 = n := by\n  simp", allTactics: true, rootGoals: true })));
    assert.ok(r.tactics.length > 0);
  });

  for (const [name, makeCache] of [
    ["replay-lazy", () => new ReplaySessionCache({ lazy: true })],
    ["replay-eager", () => new ReplaySessionCache({ lazy: false })],
    ["pickle", () => new PickleSessionCache(mkdtempSync(join(tmpdir(), "li-pickle-")))],
  ] as Array<[string, () => BaseSessionCache]>) {
    test(`session cache survives restart (${name})`, { timeout: 5 * MIN }, async () => {
      const cache = makeCache();
      await using srv = new AutoLeanServer(config, { sessionCache: cache });
      const base = cmdRes(await srv.run(command({ cmd: "def secret := 1234" }), { addToSessionCache: true }));
      assert.equal(base.env, -1);
      const thm = cmdRes(
        await srv.run(command({ cmd: "theorem s : secret = 1234 := sorry", env: base.env }), { addToSessionCache: true }),
      );
      const ps = stepRes(
        await srv.run(proofStep({ proofState: thm.sorries[0]!.proofState!, tactic: "skip" }), { addToSessionCache: true }),
      );
      assert.ok(ps.proofState < 0);
      await srv.restart();
      const r = cmdRes(await srv.run(command({ cmd: "example : secret = 1234 := rfl", env: base.env })));
      assert.ok(!r.hasErrors(), JSON.stringify(r.messages));
      const done = stepRes(await srv.run(proofStep({ proofState: ps.proofState, tactic: "rfl" })));
      assert.equal(done.proofStatus, "Completed");
      await srv.clearSessionCache(true);
      assert.ok(cache.isEmpty());
    });
  }

  test("timeout kills the REPL and the auto server recovers", { timeout: 5 * MIN }, async () => {
    await using srv = new AutoLeanServer(config);
    const start = Date.now();
    await assert.rejects(
      srv.run(command({ cmd: "partial def f (n : Nat) : Nat := f n\n#eval f 0" }), { timeout: 2000 }),
      LeanTimeoutError,
    );
    assert.ok(Date.now() - start < 6000, "timeout not respected");
    assert.ok(!srv.isAlive());
    const r = cmdRes(await srv.run(command({ cmd: "#eval 1 + 1" })));
    assert.equal(r.messages.length, 1);
    assert.equal(r.messages[0]!.data, "2");
  });

  test("pickle and unpickle", { timeout: 5 * MIN }, async () => {
    await using srv = new LeanServer(config);
    const dir = mkdtempSync(join(tmpdir(), "li-pk-"));
    const r = cmdRes(await srv.run(command({ cmd: "def pickled := 7\ntheorem pt : pickled = 7 := sorry" })));
    const envFile = join(dir, "env.olean");
    const psFile = join(dir, "ps.olean");
    cmdRes(await srv.run(pickleEnvironment({ env: r.env, pickleTo: envFile })));
    stepRes(await srv.run(pickleProofState({ proofState: r.sorries[0]!.proofState!, pickleTo: psFile })));
    await srv.restart();
    const env = cmdRes(await srv.run(unpickleEnvironment({ unpickleEnvFrom: envFile })));
    const chk = cmdRes(await srv.run(command({ cmd: "example : pickled = 7 := rfl", env: env.env })));
    assert.ok(!chk.hasErrors(), JSON.stringify(chk.messages));
    const ps = stepRes(await srv.run(unpickleProofState({ unpickleProofStateFrom: psFile, env: env.env })));
    const done = stepRes(await srv.run(proofStep({ proofState: ps.proofState, tactic: "rfl" })));
    assert.equal(done.proofStatus, "Completed");
    const bad = await srv.run(pickleEnvironment({ env: 9999, pickleTo: join(dir, "bad.olean") }));
    assert.ok(bad instanceof LeanError);
  });

  test("pool batch with shared session state", { timeout: 10 * MIN }, async () => {
    await using pool = new LeanServerPool(config, { numWorkers: 3 });
    const base = cmdRes(await pool.run(command({ cmd: "def shared := 5" })));
    assert.ok(base.env < 0);
    const reqs = Array.from({ length: 12 }, (_, i) =>
      command({ cmd: `example : shared + ${i} = ${5 + i} := rfl`, env: base.env }),
    );
    reqs.push(command({ cmd: "example : shared = 6 := rfl", env: base.env }));
    const results = await pool.runBatch(reqs, { timeoutPerCmd: 2 * MIN });
    results.forEach((res, i) => assert.equal(cmdRes(res).hasErrors(), i === reqs.length - 1, `request ${i}`));

    // Sorry proof states come back as session ids usable on every worker.
    const thm = cmdRes(await pool.run(command({ cmd: "theorem p (n : Nat) : shared + n = n + 5 := sorry", env: base.env })));
    const ps = thm.sorries[0]!.proofState!;
    assert.ok(ps < 0);
    const steps = await pool.runBatch(Array.from({ length: 6 }, () => proofStep({ proofState: ps, tactic: "(unfold shared; omega)" })));
    steps.forEach((res, i) => assert.equal(stepRes(res).proofStatus, "Completed", `step ${i}`));
  });
});
