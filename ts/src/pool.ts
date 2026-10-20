/**
 * A pool of {@link AutoLeanServer} workers sharing one {@link ReplaySessionCache}.
 *
 * Port of `lean_interact.pool`. Every request is added to the shared session cache, so the
 * returned (negative) ids can be used on any worker; the pool prefers a worker that already has
 * the requested state materialized.
 *
 * @module
 */

import { availableParallelism } from "node:os";

import type { LeanREPLConfig } from "./config.js";
import {
  requestKind,
  type CommandLikeRequest,
  CommandResponse,
  type LeanError,
  type ProofStepLikeRequest,
  type ProofStepResponse,
  type REPLRequest,
  type REPLResponse,
} from "./interface.js";
import { AutoLeanServer, type RunOptions } from "./server.js";
import { ReplaySessionCache } from "./sessioncache.js";

export interface LeanServerPoolOptions {
  /** Number of REPL processes. Default: `availableParallelism() - 1` (at least 1). */
  numWorkers?: number;
  /** Passed to each {@link AutoLeanServer}. */
  maxTotalMemory?: number;
  /** Passed to each {@link AutoLeanServer}. */
  maxProcessMemory?: number | null;
  /** Passed to each {@link AutoLeanServer}. */
  maxRestartAttempts?: number;
}

export interface RunBatchOptions {
  verbose?: boolean;
  /** Timeout per request, in milliseconds. */
  timeoutPerCmd?: number;
  /** Progress callback (replaces Python's `show_progress` tqdm bar). */
  onProgress?: (done: number, total: number) => void;
}

/**
 * @example
 * ```ts
 * await using pool = new LeanServerPool(config, { numWorkers: 4 });
 * const results = await pool.runBatch([command({ cmd: "#eval 1" }), command({ cmd: "#eval 2" })]);
 * ```
 */
export class LeanServerPool implements AsyncDisposable {
  readonly config: LeanREPLConfig;
  readonly numWorkers: number;
  readonly sessionCache = new ReplaySessionCache({ lazy: true });
  private workers: AutoLeanServer[];
  private freeWorkers: AutoLeanServer[];
  private waiters: Array<() => void> = [];

  constructor(config: LeanREPLConfig, options: LeanServerPoolOptions = {}) {
    this.config = config;
    this.numWorkers = options.numWorkers || Math.max(1, availableParallelism() - 1);
    this.workers = Array.from(
      { length: this.numWorkers },
      () =>
        new AutoLeanServer(config, {
          maxTotalMemory: options.maxTotalMemory,
          maxProcessMemory: options.maxProcessMemory,
          maxRestartAttempts: options.maxRestartAttempts,
          sessionCache: this.sessionCache,
        }),
    );
    this.freeWorkers = [...this.workers];
  }

  /** Kill all workers. */
  async close(): Promise<void> {
    const toKill = this.workers;
    this.workers = [];
    this.freeWorkers = [];
    await Promise.all(toKill.map((w) => w.kill().catch(() => undefined)));
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.close();
  }

  private tryAcquire(requiredSessionId: number | undefined): AutoLeanServer | null {
    if (this.freeWorkers.length === 0) return null;
    if (requiredSessionId !== undefined && requiredSessionId < 0) {
      const i = this.freeWorkers.findIndex((w) => this.sessionCache.isMaterialized(requiredSessionId, w));
      if (i !== -1) return this.freeWorkers.splice(i, 1)[0];
    }
    return this.freeWorkers.pop()!; // most recently released (LIFO)
  }

  private async acquire(requiredSessionId: number | undefined): Promise<AutoLeanServer> {
    for (;;) {
      if (this.workers.length === 0) throw new Error("The Lean server pool is closed.");
      const worker = this.tryAcquire(requiredSessionId);
      if (worker) return worker;
      await new Promise<void>((resolve) => this.waiters.push(resolve));
    }
  }

  private release(worker: AutoLeanServer): void {
    if (!this.workers.includes(worker)) return;
    this.freeWorkers.push(worker);
    this.waiters.shift()?.();
  }

  /** Run a request on an available worker (always added to the shared session cache). */
  run(request: CommandLikeRequest, options?: RunOptions): Promise<CommandResponse | LeanError>;
  run(request: ProofStepLikeRequest, options?: RunOptions): Promise<ProofStepResponse | LeanError>;
  run(request: REPLRequest, options?: RunOptions): Promise<REPLResponse | LeanError>;
  async run(request: REPLRequest, options: RunOptions = {}): Promise<REPLResponse | LeanError> {
    const kind = requestKind(request);
    let reqId: number | undefined;
    if (kind === "Command") reqId = (request as { env?: number }).env;
    else if (kind === "ProofStep") reqId = (request as { proofState: number }).proofState;

    const worker = await this.acquire(reqId);
    try {
      const response = await worker.run(request, { ...options, addToSessionCache: true });
      if (response instanceof CommandResponse) {
        // Sorry proof states are REPL ids local to `worker`; replace them with session ids so
        // follow-up proof steps work on any worker. Upstream returns the raw ids, which silently
        // target the wrong worker.
        this.sessionCache.addSorryStates(response.env, worker).forEach((id, i) => {
          if (id !== undefined) response.sorries[i]!.proofState = id;
        });
      }
      return response;
    } finally {
      this.release(worker);
    }
  }

  /** Alias of {@link run}. */
  asyncRun(request: REPLRequest, options?: RunOptions): Promise<REPLResponse | LeanError> {
    return this.run(request, options);
  }

  /**
   * Run requests concurrently on the workers. Results are in request order; a request that threw
   * yields its `Error` instead of failing the whole batch.
   */
  async runBatch(requests: REPLRequest[], options: RunBatchOptions = {}): Promise<Array<REPLResponse | LeanError | Error>> {
    let done = 0;
    return Promise.all(
      requests.map(async (req) => {
        let result: REPLResponse | LeanError | Error;
        try {
          result = await this.run(req, { verbose: options.verbose, timeout: options.timeoutPerCmd });
        } catch (err) {
          result = err instanceof Error ? err : new Error(String(err));
        }
        options.onProgress?.(++done, requests.length);
        return result;
      }),
    );
  }

  /** Alias of {@link runBatch}. */
  asyncRunBatch(requests: REPLRequest[], options?: RunBatchOptions): Promise<Array<REPLResponse | LeanError | Error>> {
    return this.runBatch(requests, options);
  }
}
