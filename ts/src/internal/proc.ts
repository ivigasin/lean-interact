/** Process-tree memory measurement and termination (replacement for psutil). */

import { execFile, spawn } from "node:child_process";
import { promises as fs } from "node:fs";
import { freemem, totalmem } from "node:os";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

interface ProcInfo {
  pid: number;
  ppid: number;
  rssBytes: number;
}

async function listProcessesLinux(): Promise<ProcInfo[]> {
  const out: ProcInfo[] = [];
  for (const entry of await fs.readdir("/proc")) {
    if (!/^\d+$/.test(entry)) continue;
    try {
      const status = await fs.readFile(`/proc/${entry}/status`, "utf8");
      const ppid = Number(/^PPid:\s+(\d+)/m.exec(status)?.[1] ?? 0);
      const rssKb = Number(/^VmRSS:\s+(\d+)/m.exec(status)?.[1] ?? 0);
      out.push({ pid: Number(entry), ppid, rssBytes: rssKb * 1024 });
    } catch {
      // process exited while scanning
    }
  }
  return out;
}

async function listProcessesPs(): Promise<ProcInfo[]> {
  const { stdout } = await execFileAsync("ps", ["-A", "-o", "pid=,ppid=,rss="], { maxBuffer: 64 * 1024 * 1024 });
  const out: ProcInfo[] = [];
  for (const line of stdout.split("\n")) {
    const parts = line.trim().split(/\s+/);
    if (parts.length < 3) continue;
    out.push({ pid: Number(parts[0]), ppid: Number(parts[1]), rssBytes: Number(parts[2]) * 1024 });
  }
  return out;
}

/** Total resident memory (bytes) of a process and all its descendants. 0 if unavailable. */
export async function getTotalMemoryUsage(pid: number): Promise<number> {
  if (process.platform === "win32") return 0;
  let procs: ProcInfo[];
  try {
    procs = process.platform === "linux" ? await listProcessesLinux() : await listProcessesPs();
  } catch {
    return 0;
  }
  const children = new Map<number, ProcInfo[]>();
  let root: ProcInfo | undefined;
  for (const p of procs) {
    if (p.pid === pid) root = p;
    const list = children.get(p.ppid) ?? [];
    list.push(p);
    children.set(p.ppid, list);
  }
  if (!root) return 0;
  let total = 0;
  const stack = [root];
  const seen = new Set<number>();
  while (stack.length) {
    const p = stack.pop()!;
    if (seen.has(p.pid)) continue;
    seen.add(p.pid);
    total += p.rssBytes;
    stack.push(...(children.get(p.pid) ?? []));
  }
  return total;
}

/** System-wide used memory in percent (psutil `virtual_memory().percent`). */
export async function getSystemMemoryPercent(): Promise<number> {
  const total = totalmem();
  try {
    if (process.platform === "linux") {
      const info = await fs.readFile("/proc/meminfo", "utf8");
      const memTotal = Number(/^MemTotal:\s+(\d+)/m.exec(info)?.[1]);
      const avail = Number(/^MemAvailable:\s+(\d+)/m.exec(info)?.[1]);
      if (memTotal > 0 && Number.isFinite(avail)) return (100 * (memTotal - avail)) / memTotal;
    } else if (process.platform === "darwin") {
      const { stdout } = await execFileAsync("vm_stat");
      const pageSize = Number(/page size of (\d+) bytes/.exec(stdout)?.[1] ?? 4096);
      const pages = (label: string) => Number(new RegExp(`^${label}:\\s+(\\d+)`, "m").exec(stdout)?.[1] ?? 0);
      const avail = (pages("Pages free") + pages("Pages inactive")) * pageSize;
      return (100 * (total - avail)) / total;
    }
  } catch {
    // fall through
  }
  return (100 * (total - freemem())) / total;
}

/**
 * Terminate a process group led by `pid` (SIGTERM, then SIGKILL after `graceMs`).
 * Resolves once `exited` resolves or the grace period elapses.
 */
export async function killProcessTree(pid: number, exited: Promise<unknown>, graceMs = 1000): Promise<void> {
  if (process.platform === "win32") {
    await new Promise<void>((resolve) => {
      const p = spawn("taskkill", ["/pid", String(pid), "/T", "/F"], { stdio: "ignore", windowsHide: true });
      p.once("error", () => resolve());
      p.once("close", () => resolve());
    });
    return;
  }
  const signal = (sig: NodeJS.Signals) => {
    try {
      process.kill(-pid, sig);
    } catch {
      try {
        process.kill(pid, sig);
      } catch {
        // already gone
      }
    }
  };
  signal("SIGTERM");
  let timer: NodeJS.Timeout | undefined;
  const timedOut = await Promise.race([
    exited.then(() => false),
    new Promise<boolean>((resolve) => (timer = setTimeout(() => resolve(true), graceMs))),
  ]);
  clearTimeout(timer);
  // Kill the rest of the group even if the leader exited on SIGTERM (e.g. `lake env` children).
  signal("SIGKILL");
  if (timedOut) await Promise.race([exited, new Promise((r) => setTimeout(r, graceMs))]);
}

/** Synchronous best-effort SIGKILL of a process group; used on interpreter exit. */
export function killProcessTreeSync(pid: number): void {
  if (process.platform === "win32") {
    try {
      process.kill(pid);
    } catch {
      // ignore
    }
    return;
  }
  try {
    process.kill(-pid, "SIGKILL");
  } catch {
    try {
      process.kill(pid, "SIGKILL");
    } catch {
      // ignore
    }
  }
}
