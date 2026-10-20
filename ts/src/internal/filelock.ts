/**
 * Cross-process file lock (replacement for Python's `filelock.FileLock`).
 *
 * The lock is a file created with `O_EXCL` that records the owner's pid and host. A lock left
 * behind by a dead process on the same host is detected and broken. Locks are not reentrant: a
 * second acquisition from the same process waits like any other contender.
 */

import { promises as fs, constants as fsConstants } from "node:fs";
import { hostname } from "node:os";
import { dirname } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

/** Thrown when a lock cannot be acquired in time. */
export class LockTimeoutError extends Error {
  constructor(readonly lockPath: string) {
    super(`Timed out waiting for lock ${lockPath}`);
    this.name = "LockTimeoutError";
  }
}

const POLL_MS = 100;
/** A lock file without a readable owner is considered stale after this long. */
const ORPHAN_MS = 30_000;

function pidAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === "EPERM";
  }
}

async function isStale(lockPath: string): Promise<boolean> {
  let content: string;
  let mtimeMs: number;
  try {
    content = await fs.readFile(lockPath, "utf8");
    mtimeMs = (await fs.stat(lockPath)).mtimeMs;
  } catch {
    return false; // vanished: retry immediately
  }
  const [pidStr, host] = content.trim().split("@");
  const pid = Number(pidStr);
  if (!Number.isInteger(pid) || pid <= 0) return Date.now() - mtimeMs > ORPHAN_MS;
  if (host !== hostname()) return false; // cannot check remote hosts
  return !pidAlive(pid);
}

export class FileLock {
  private held = false;

  constructor(readonly lockPath: string) {}

  /**
   * Acquire the lock.
   * @param timeoutMs Give up after this many milliseconds; `undefined` or negative waits forever.
   */
  async acquire(timeoutMs?: number): Promise<void> {
    const deadline = timeoutMs !== undefined && timeoutMs >= 0 ? Date.now() + timeoutMs : Infinity;
    await fs.mkdir(dirname(this.lockPath), { recursive: true });
    for (;;) {
      try {
        const fh = await fs.open(this.lockPath, fsConstants.O_CREAT | fsConstants.O_EXCL | fsConstants.O_WRONLY);
        try {
          await fh.writeFile(`${process.pid}@${hostname()}`);
        } finally {
          await fh.close();
        }
        this.held = true;
        return;
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code !== "EEXIST") throw err;
      }
      if (await isStale(this.lockPath)) {
        await fs.rm(this.lockPath, { force: true });
        continue;
      }
      if (Date.now() >= deadline) throw new LockTimeoutError(this.lockPath);
      await sleep(POLL_MS);
    }
  }

  async release(): Promise<void> {
    if (!this.held) return;
    this.held = false;
    await fs.rm(this.lockPath, { force: true });
  }
}

/** Run `fn` while holding the lock at `lockPath`. */
export async function withFileLock<T>(lockPath: string, fn: () => Promise<T>, timeoutMs?: number): Promise<T> {
  const lock = new FileLock(lockPath);
  await lock.acquire(timeoutMs);
  try {
    return await fn();
  } finally {
    await lock.release();
  }
}
