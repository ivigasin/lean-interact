/**
 * `LeanServer` and `AutoLeanServer`: drive a Lean REPL process over its JSON protocol.
 *
 * Port of `lean_interact.server`. `LeanServer` is a thin wrapper around the REPL; `AutoLeanServer`
 * adds automatic restarts (memory pressure, timeouts, crashes) and a session cache whose states
 * survive restarts.
 *
 * @module
 */

import { spawn, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import type { Socket } from "node:net";
import { setTimeout as sleep } from "node:timers/promises";

import type { LeanREPLConfig } from "./config.js";
import {
  CommandResponse,
  isCommandLikeKind,
  LeanError,
  ProofStepResponse,
  REQUEST_KIND,
  requestKind,
  requestToDict,
  type CommandLikeRequest,
  type Name,
  type Options,
  type ProofStepLikeRequest,
  type REPLRequest,
  type REPLResponse,
} from "./interface.js";
import { logger } from "./internal/log.js";
import { getSystemMemoryPercent, getTotalMemoryUsage, killProcessTree, killProcessTreeSync } from "./internal/proc.js";
import { ReplaySessionCache, type BaseSessionCache } from "./sessioncache.js";

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** The REPL did not answer in time (it has been killed). Python: `TimeoutError`. */
export class LeanTimeoutError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TimeoutError";
  }
}

/** The REPL closed unexpectedly. Python: `ConnectionAbortedError`. */
export class LeanServerClosedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConnectionAbortedError";
  }
}

/** The REPL is not running. Python: `ChildProcessError`. */
export class LeanServerNotRunningError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ChildProcessError";
  }
}

/** Memory stayed too high after the maximum number of restarts. Python: `MemoryError`. */
export class LeanMemoryError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "MemoryError";
  }
}

/** The REPL output was not valid JSON. Python: `json.JSONDecodeError`. */
export class LeanOutputParseError extends Error {
  constructor(
    message: string,
    readonly rawOutput: string,
  ) {
    super(message);
    this.name = "JSONDecodeError";
  }
}

// ---------------------------------------------------------------------------
// LeanServer
// ---------------------------------------------------------------------------

export interface RunOptions {
  /** Log the query and the raw output. */
  verbose?: boolean;
  /** Timeout in milliseconds. On expiry the REPL is killed. Default: none. */
  timeout?: number;
  /** Abort the request. The REPL is killed (its output stream would be out of sync otherwise). */
  signal?: AbortSignal;
}

const SEPARATOR = "-".repeat(50);
const MAX_STDERR = 1 << 20;

/** Servers alive in this process, killed when Node exits. */
const liveServers = new Set<LeanServer>();
let exitHookInstalled = false;

function installExitHook(): void {
  if (exitHookInstalled) return;
  exitHookInstalled = true;
  process.once("exit", () => {
    for (const server of liveServers) server.killSync();
  });
}

interface Pending {
  resolve(output: string): void;
  reject(err: Error): void;
}

/**
 * Wrapper around a Lean REPL process.
 *
 * Requests are serialized: only one is sent to the REPL at a time. The process does not keep Node
 * alive while idle, and is killed when Node exits; call {@link kill} (or use `await using`) to stop
 * it explicitly.
 *
 * @example
 * ```ts
 * const config = await LeanREPLConfig.create();
 * await using server = new LeanServer(config);
 * const res = await server.run(command({ cmd: "theorem ex (n : Nat) : n = 5 → n = 5 := id" }));
 * ```
 */
export class LeanServer implements AsyncDisposable {
  readonly config: LeanREPLConfig;
  /** Unique id of this server instance (session caches key REPL ids by it). */
  readonly id: string = randomUUID();

  protected proc: ChildProcess | null = null;
  private exited: Promise<void> = Promise.resolve();
  private stdoutBuffer = "";
  private stderrBuffer = "";
  private stdoutEnded = false;
  private spawnError: Error | null = null;
  private pending: Pending | null = null;
  private queue: Promise<unknown> = Promise.resolve();

  /** Start a Lean REPL process for `config`. */
  constructor(config: LeanREPLConfig) {
    if (!config.isSetup()) throw new Error("The Lean environment has not been set up properly.");
    this.config = config;
    this.start();
  }

  /** Lean version of the REPL. */
  get leanVersion(): string | null {
    return this.config.leanVersion;
  }

  /** PID of the REPL process group leader, if running. */
  get pid(): number | undefined {
    return this.proc?.pid;
  }

  /** Start the REPL process. Called by the constructor. */
  start(): void {
    const replBin = this.config.replBinaryPath;
    let file = this.config.lakePath;
    let args = ["env", replBin];
    const limit = this.config.memoryHardLimitMb;
    if (process.platform === "linux" && limit !== null && limit !== undefined) {
      // Node has no preexec hook: apply RLIMIT_AS through the shell before exec'ing lake.
      args = ["-c", `ulimit -v ${Math.floor(limit * 1024)}; exec "$@"`, "sh", file, ...args];
      file = "/bin/sh";
    }

    this.stdoutBuffer = "";
    this.stderrBuffer = "";
    this.stdoutEnded = false;
    this.spawnError = null;

    const child = spawn(file, args, {
      cwd: this.config.workingDir,
      stdio: ["pipe", "pipe", "pipe"],
      detached: process.platform !== "win32",
      windowsHide: true,
    });
    this.proc = child;
    this.exited = new Promise<void>((resolve) => child.once("close", () => resolve()));

    child.stdout!.setEncoding("utf8");
    child.stderr!.setEncoding("utf8");
    child.stdout!.on("data", (chunk: string) => {
      if (this.proc !== child) return;
      this.stdoutBuffer += chunk;
      this.flushPending();
    });
    child.stdout!.once("end", () => {
      if (this.proc !== child) return;
      this.stdoutEnded = true;
      this.flushPending();
    });
    child.stderr!.on("data", (chunk: string) => {
      if (this.proc !== child) return;
      this.stderrBuffer = (this.stderrBuffer + chunk).slice(-MAX_STDERR);
    });
    child.stdin!.on("error", () => {
      // EPIPE when the REPL dies; surfaced through the closed stdout instead.
    });
    child.once("error", (err) => {
      if (this.proc !== child) return;
      this.spawnError = err;
      this.stdoutEnded = true;
      this.flushPending();
    });
    child.once("exit", () => {
      if (this.proc !== child) return;
      this.stdoutEnded = true;
      // Let buffered stdout drain before resolving a pending request with partial output.
      setImmediate(() => this.flushPending());
    });

    this.setRef(false);
    liveServers.add(this);
    installExitHook();
  }

  /** Keep Node alive only while a request is in flight. */
  private setRef(ref: boolean): void {
    const child = this.proc;
    if (!child) return;
    for (const stream of [child.stdin, child.stdout, child.stderr] as unknown as Array<Socket | null>) {
      if (stream && typeof stream.ref === "function") {
        if (ref) stream.ref();
        else stream.unref();
      }
    }
    if (ref) child.ref();
    else child.unref();
  }

  private flushPending(): void {
    if (!this.pending) return;
    const idx = this.stdoutBuffer.indexOf("\n\n");
    if (idx !== -1) {
      const output = this.stdoutBuffer.slice(0, idx + 2);
      this.stdoutBuffer = this.stdoutBuffer.slice(idx + 2);
      const p = this.pending;
      this.pending = null;
      p.resolve(output);
    } else if (this.stdoutEnded) {
      const output = this.stdoutBuffer;
      this.stdoutBuffer = "";
      const p = this.pending;
      this.pending = null;
      p.resolve(output);
    }
  }

  /** stdout not consumed by a request, and the last MB of stderr. */
  getStdoutStderr(): { stdout: string; stderr: string } {
    const stderr = this.stderrBuffer + (this.spawnError ? `\n${this.spawnError.message}` : "");
    return { stdout: this.stdoutBuffer, stderr };
  }

  /** Whether the REPL process is running. */
  isAlive(): boolean {
    const p = this.proc;
    return p !== null && p.exitCode === null && p.signalCode === null && !this.spawnError && p.pid !== undefined;
  }

  /** Kill the REPL process and its process group. */
  async kill(): Promise<void> {
    const child = this.proc;
    if (!child) return;
    const exited = this.exited;
    this.proc = null;
    liveServers.delete(this);
    const pending = this.pending;
    this.pending = null;
    pending?.resolve("");
    child.stdin?.destroy();
    if (child.pid !== undefined && child.exitCode === null && child.signalCode === null) {
      await killProcessTree(child.pid, exited);
    } else if (child.pid !== undefined) {
      killProcessTreeSync(child.pid); // leader gone, but children of `lake env` may remain
    }
    child.stdout?.destroy();
    child.stderr?.destroy();
  }

  /** @internal Synchronous SIGKILL, used on process exit. */
  killSync(): void {
    if (this.proc?.pid !== undefined) killProcessTreeSync(this.proc.pid);
  }

  /** Restart the REPL process. */
  async restart(): Promise<void> {
    await this.kill();
    this.start();
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.kill();
  }

  /** Resident memory (MB) of the REPL process tree. */
  async getMemoryUsage(): Promise<number> {
    const pid = this.proc?.pid;
    if (pid === undefined) return 0;
    return (await getTotalMemoryUsage(pid)) / 1024 ** 2;
  }

  /** Run `fn` after all previously queued requests (Python's `threading.Lock`). */
  private serialize<T>(fn: () => Promise<T>): Promise<T> {
    const result = this.queue.then(fn, fn);
    this.queue = result.catch(() => undefined);
    return result;
  }

  private executeCmdInRepl(jsonQuery: string, options: RunOptions): Promise<string> {
    return this.serialize(async () => {
      const child = this.proc;
      if (!child || !child.stdin) throw new LeanServerClosedError("The Lean server is not running.");
      if (options.signal?.aborted) {
        await this.kill();
        throw options.signal.reason instanceof Error ? options.signal.reason : new Error("Aborted");
      }
      if (options.verbose) logger.info("Sending query: %s", jsonQuery);

      this.setRef(true);
      let timer: NodeJS.Timeout | undefined;
      let onAbort: (() => void) | undefined;
      try {
        const output = await new Promise<string>((resolve, reject) => {
          this.pending = { resolve, reject };
          if (options.timeout !== undefined && options.timeout !== null) {
            timer = setTimeout(() => {
              this.pending = null;
              reject(
                new LeanTimeoutError(
                  `The Lean server did not respond in time (timeout=${options.timeout}ms) and is now killed.`,
                ),
              );
            }, options.timeout);
          }
          if (options.signal) {
            onAbort = () => {
              this.pending = null;
              const reason = options.signal!.reason;
              reject(reason instanceof Error ? reason : new Error("Aborted"));
            };
            options.signal.addEventListener("abort", onAbort, { once: true });
          }
          child.stdin!.write(jsonQuery + "\n\n");
          this.flushPending();
        });
        if (output.trim()) return output;
        throw new LeanServerClosedError("The Lean server returned no output.");
      } catch (err) {
        if (!(err instanceof LeanServerClosedError)) await this.kill();
        throw err;
      } finally {
        clearTimeout(timer);
        if (onAbort) options.signal!.removeEventListener("abort", onAbort);
        this.setRef(false);
      }
    });
  }

  private parseReplOutput(rawOutput: string, verbose?: boolean): Record<string, unknown> {
    if (verbose) logger.info("Server output: `%s`", rawOutput);
    try {
      return JSON.parse(rawOutput) as Record<string, unknown>;
    } catch (err) {
      throw new LeanOutputParseError(`Could not parse the Lean server output: \`${JSON.stringify(rawOutput)}\`.`, rawOutput);
    }
  }

  /** `LeanServer.run_dict` implementation, shared with {@link AutoLeanServer}. */
  protected async rawRunDict(request: Record<string, unknown>, options: RunOptions = {}): Promise<Record<string, unknown>> {
    if (!this.isAlive()) {
      const { stdout, stderr } = this.getStdoutStderr();
      throw new LeanServerNotRunningError(
        `The Lean server is not running.\n${SEPARATOR}\nstdout:\n${stdout}\n${SEPARATOR}\nstderr:\n${stderr}\n${SEPARATOR}`,
      );
    }
    const jsonQuery = JSON.stringify(request);
    let rawOutput: string;
    try {
      rawOutput = await this.executeCmdInRepl(jsonQuery, options);
    } catch (err) {
      if (err instanceof LeanServerClosedError) {
        const { stdout, stderr } = this.getStdoutStderr();
        await this.kill();
        throw new LeanServerClosedError(
          `The Lean server closed unexpectedly.\n${SEPARATOR}\nstdout:\n${stdout}\n${SEPARATOR}\nstderr:\n${stderr}\n${SEPARATOR}\n` +
            "If stdout and stderr are empty or obscure, here is a list of possible reasons (not exhaustive):\n" +
            "- Not enough memory and/or compute available\n" +
            "- The cached Lean REPL is corrupted. In this case, clear the cache using `lean-interact clear-cache`.\n" +
            "- An uncaught exception in the Lean REPL",
        );
      }
      throw err;
    }
    return this.parseReplOutput(rawOutput, options.verbose);
  }

  /**
   * Send a raw JSON request and return the raw JSON answer. Useful with custom REPLs whose
   * interface differs from the standard one.
   *
   * @example
   * ```ts
   * const result = await server.runDict({ cmd: "your Lean code here" });
   * ```
   */
  async runDict(request: Record<string, unknown>, options: RunOptions = {}): Promise<Record<string, unknown>> {
    return this.rawRunDict(request, options);
  }

  /** Apply config-level options (incrementality, parallel elaboration) to command requests. */
  protected augmentRequest<T extends REPLRequest>(request: T): T {
    const kind = requestKind(request);
    if (kind !== "Command" && kind !== "FileCommand") return request;
    let out = { ...request, [REQUEST_KIND]: kind } as T & { incrementality?: boolean; setOptions?: Options };
    if (this.config.enableIncrementalOptimization) out = { ...out, incrementality: true };
    if (this.config.enableParallelElaboration) {
      const setOptions: Options = out.setOptions ? [...out.setOptions] : [];
      if (!setOptions.some(([name, value]) => isElabAsync(name) && value === true)) {
        setOptions.push([["Elab", "async"], true]);
      }
      out = { ...out, setOptions };
    }
    return out;
  }

  /**
   * Run a REPL request. REPL-level errors are returned as {@link LeanError}; transport failures
   * (timeout, crash) are thrown.
   */
  run(request: CommandLikeRequest, options?: RunOptions): Promise<CommandResponse | LeanError>;
  run(request: ProofStepLikeRequest, options?: RunOptions): Promise<ProofStepResponse | LeanError>;
  run(request: REPLRequest, options?: RunOptions): Promise<REPLResponse | LeanError>;
  async run(request: REPLRequest, options: RunOptions = {}): Promise<REPLResponse | LeanError> {
    const augmented = this.augmentRequest(request);
    const result = await this.rawRunDict(requestToDict(augmented), options);
    return parseResponse(augmented, result, false);
  }

  /** Alias of {@link run} (Python's `async_run`; every call is asynchronous in TypeScript). */
  asyncRun(request: CommandLikeRequest, options?: RunOptions): Promise<CommandResponse | LeanError>;
  asyncRun(request: ProofStepLikeRequest, options?: RunOptions): Promise<ProofStepResponse | LeanError>;
  asyncRun(request: REPLRequest, options?: RunOptions): Promise<REPLResponse | LeanError> {
    return this.run(request, options);
  }
}

function isElabAsync(name: Name): boolean {
  return name.length === 2 && name[0] === "Elab" && name[1] === "async";
}

function parseResponse(request: REPLRequest, result: Record<string, unknown>, emptyIsError: boolean): REPLResponse | LeanError {
  const keys = Object.keys(result);
  if ((keys.length === 1 && keys[0] === "message") || (emptyIsError && keys.length === 0)) {
    return new LeanError(result as { message: string });
  }
  if (isCommandLikeKind(requestKind(request))) return new CommandResponse(result as { env: number });
  return new ProofStepResponse(result as { proofStatus: string; proofState: number });
}

// ---------------------------------------------------------------------------
// AutoLeanServer
// ---------------------------------------------------------------------------

export interface AutoLeanServerOptions {
  /** Restart when system-wide memory usage reaches this fraction (0-1). Default `0.8`. */
  maxTotalMemory?: number;
  /**
   * Restart when the REPL uses this fraction of `config.memoryHardLimitMb` (only with a hard
   * limit). Default `0.8`; `null` disables.
   */
  maxProcessMemory?: number | null;
  /** Consecutive restarts before throwing {@link LeanMemoryError}. Default `5`. */
  maxRestartAttempts?: number;
  /** Session cache. Default: a new {@link ReplaySessionCache}. */
  sessionCache?: BaseSessionCache;
}

export interface AutoRunOptions extends RunOptions {
  /**
   * Store the resulting state in the session cache. The response then carries a negative
   * session id (`env` or `proofState`) that remains valid across restarts.
   */
  addToSessionCache?: boolean;
}

/**
 * A {@link LeanServer} that restarts automatically on memory pressure, timeouts and crashes, with
 * exponential backoff between restarts. States added to its session cache (negative ids starting
 * at -1) survive restarts.
 *
 * @example
 * ```ts
 * await using server = new AutoLeanServer(config);
 * const res = await server.run(command({ cmd: "def x := 1" }), { addToSessionCache: true }); // env -1
 * await server.run(command({ cmd: "#eval x", env: -1 }));
 * ```
 */
export class AutoLeanServer extends LeanServer {
  readonly sessionCache: BaseSessionCache;
  private readonly maxTotalMemory: number;
  private readonly maxProcessMemory: number | null;
  private readonly maxRestartAttempts: number;

  constructor(config: LeanREPLConfig, options: AutoLeanServerOptions = {}) {
    super(config);
    this.sessionCache = options.sessionCache ?? new ReplaySessionCache();
    this.maxTotalMemory = options.maxTotalMemory ?? 0.8;
    this.maxProcessMemory = options.maxProcessMemory === undefined ? 0.8 : options.maxProcessMemory;
    this.maxRestartAttempts = options.maxRestartAttempts ?? 5;
  }

  private async getReplStateId(stateId: number | undefined): Promise<number | undefined> {
    if (stateId === undefined || stateId === null) return undefined;
    if (this.sessionCache.has(stateId)) return this.sessionCache.getReplId(stateId, this);
    return stateId;
  }

  /** Restart the REPL and reload the session cache. */
  override async restart(options: { verbose?: boolean } = {}): Promise<void> {
    await super.restart();
    await this.sessionCache.reload(this, undefined, options);
  }

  /** Remove a state from the session cache. */
  async removeFromSessionCache(sessionStateId: number): Promise<void> {
    await this.sessionCache.remove(sessionStateId);
  }

  /**
   * Clear the session cache. With `force`, restart immediately to free the REPL memory; otherwise
   * memory is reclaimed at the next restart.
   */
  async clearSessionCache(force = false): Promise<void> {
    await this.sessionCache.clear();
    if (force) await this.restart();
  }

  /** Delay before the `attempt`-th memory restart: `2^attempt` seconds, as in Python. */
  protected backoffDelayMs(attempt: number): number {
    return 2 ** attempt * 1000;
  }

  private async memoryTooHigh(): Promise<boolean> {
    if ((await getSystemMemoryPercent()) >= 100 * this.maxTotalMemory) return true;
    const limit = this.config.memoryHardLimitMb;
    if (this.isAlive() && this.pid !== undefined && limit !== null && this.maxProcessMemory !== null) {
      return (await getTotalMemoryUsage(this.pid)) >= this.maxProcessMemory * limit * 1024 ** 2;
    }
    return false;
  }

  private async runDictBackoff(request: Record<string, unknown>, options: RunOptions): Promise<Record<string, unknown>> {
    for (let attempt = 0; await this.memoryTooHigh(); attempt++) {
      await this.kill();
      if (attempt >= this.maxRestartAttempts) {
        throw new LeanMemoryError(
          `Memory usage is too high. We attempted to restart the Lean server ${this.maxRestartAttempts} times without success.`,
        );
      }
      if (options.verbose) logger.info("Memory usage is too high. Reloading the Lean server...");
      await sleep(this.backoffDelayMs(attempt));
    }

    if (!this.isAlive()) {
      this.start();
      await this.sessionCache.reload(this, undefined, { verbose: options.verbose });
    }

    // Replace negative session ids with the corresponding REPL ids.
    let req = request;
    if (typeof req.env === "number" && req.env < 0) req = { ...req, env: await this.getReplStateId(req.env) };
    if (typeof req.proofState === "number" && req.proofState < 0) {
      req = { ...req, proofState: await this.getReplStateId(req.proofState) };
    }
    return this.rawRunDict(requestToDict(req), options);
  }

  /** Not available with automated memory management: use {@link run}. */
  override async runDict(_request: Record<string, unknown>, _options?: RunOptions): Promise<Record<string, unknown>> {
    throw new Error(
      "This method is not available with automated memory management. Please use `run`, or use `runDict` from the `LeanServer` class.",
    );
  }

  override run(request: CommandLikeRequest, options?: AutoRunOptions): Promise<CommandResponse | LeanError>;
  override run(request: ProofStepLikeRequest, options?: AutoRunOptions): Promise<ProofStepResponse | LeanError>;
  override run(request: REPLRequest, options?: AutoRunOptions): Promise<REPLResponse | LeanError>;
  override async run(request: REPLRequest, options: AutoRunOptions = {}): Promise<REPLResponse | LeanError> {
    const augmented = this.augmentRequest(request);
    const result = await this.runDictBackoff(requestToDict(augmented), options);
    const response = parseResponse(augmented, result, true);
    if (!options.addToSessionCache || response instanceof LeanError) return response;
    const sessionId = await this.sessionCache.add(this, augmented, response, { verbose: options.verbose });
    return response instanceof CommandResponse ? response.with({ env: sessionId }) : response.with({ proofState: sessionId });
  }

  override asyncRun(request: CommandLikeRequest, options?: AutoRunOptions): Promise<CommandResponse | LeanError>;
  override asyncRun(request: ProofStepLikeRequest, options?: AutoRunOptions): Promise<ProofStepResponse | LeanError>;
  override asyncRun(request: REPLRequest, options?: AutoRunOptions): Promise<REPLResponse | LeanError> {
    return this.run(request, options);
  }
}
