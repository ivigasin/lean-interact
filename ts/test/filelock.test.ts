import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { FileLock, LockTimeoutError, withFileLock } from "../src/index.js";

const dir = mkdtempSync(join(tmpdir(), "lean-interact-lock-"));

test("mutual exclusion", async () => {
  const path = join(dir, "a.lock");
  const order: string[] = [];
  await Promise.all(
    [1, 2, 3].map((i) =>
      withFileLock(path, async () => {
        order.push(`start${i}`);
        await new Promise((r) => setTimeout(r, 50));
        order.push(`end${i}`);
      }),
    ),
  );
  for (let i = 0; i < order.length; i += 2) assert.equal(order[i].slice(5), order[i + 1].slice(3));
});

test("timeout", async () => {
  const path = join(dir, "b.lock");
  const held = new FileLock(path);
  await held.acquire();
  await assert.rejects(new FileLock(path).acquire(150), LockTimeoutError);
  await held.release();
  await new FileLock(path).acquire(150);
});

test("stale lock from a dead process is broken", async () => {
  const path = join(dir, "c.lock");
  const { hostname } = await import("node:os");
  writeFileSync(path, `999999@${hostname()}`);
  const lock = new FileLock(path);
  await lock.acquire(500);
  await lock.release();
});
