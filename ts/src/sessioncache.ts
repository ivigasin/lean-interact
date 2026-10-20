/**
 * Session caches: store environments / proof states so they survive REPL restarts.
 *
 * Port of `lean_interact.sessioncache`. Used by {@link AutoLeanServer}; a single cache can be
 * shared by several servers (each server gets its own REPL ids for the same session state).
 *
 * @example
 * ```ts
 * const replay = new ReplaySessionCache();
 * const pickle = new PickleSessionCache("./cache");
 * const server = new AutoLeanServer(config, { sessionCache: replay });
 * ```
 *
 * @module
 */

import { createHash, randomUUID } from "node:crypto";
import { existsSync, promises as fs } from "node:fs";
import { join } from "node:path";

import {
  cloneRequest,
  CommandResponse,
  LeanError,
  pickleEnvironment,
  pickleProofState,
  ProofStepResponse,
  requestKind,
  unpickleEnvironment,
  unpickleProofState,
  type BaseREPLResponse,
  type REPLRequest,
  type REPLResponse,
} from "./interface.js";
import { withFileLock } from "./internal/filelock.js";

/** What a session cache needs from a server (implemented by `AutoLeanServer`). */
export interface CacheServer {
  /** Unique per server instance. */
  readonly id: string;
  run(request: REPLRequest, options?: { verbose?: boolean; timeout?: number }): Promise<REPLResponse | LeanError>;
}

/** A cached session state. */
export interface SessionState {
  /** Negative session id. */
  sessionId: number;
  isProofState: boolean;
  /** REPL id of this state on each server (by server id); `undefined` when not materialized. */
  replIds: Map<string, number | undefined>;
}

export interface VerboseOption {
  verbose?: boolean;
}

/** Interface of session caches. */
export abstract class BaseSessionCache implements Iterable<SessionState> {
  /**
   * Store the state produced by `request` / `response`.
   * @returns A negative session state id usable in place of `env` / `proofState`.
   */
  abstract add(server: CacheServer, request: REPLRequest, response: BaseREPLResponse, options?: VerboseOption): Promise<number>;
  /** Remove a state. */
  abstract remove(sessionStateId: number, options?: VerboseOption): Promise<void>;
  /** Re-establish states on a (re)started server. */
  abstract reload(server: CacheServer, timeoutPerStateMs: number | undefined, options?: VerboseOption): Promise<void>;
  abstract isEmpty(): boolean;
  /** Remove all states. */
  abstract clear(options?: VerboseOption): Promise<void>;
  abstract [Symbol.iterator](): Iterator<SessionState>;
  /** Python `__contains__`. */
  abstract has(sessionId: number): boolean;
  /** Python `__getitem__`. Throws if absent. */
  abstract get(sessionId: number): SessionState;
  /** All session state ids. */
  abstract keys(): number[];
  /** REPL id of a session state on `server`, materializing it if needed. */
  abstract getReplId(sessionStateId: number, server: CacheServer): Promise<number | undefined>;
}

function stateIdOf(response: BaseREPLResponse): { replId: number; isProofState: boolean } {
  if (response instanceof ProofStepResponse) return { replId: response.proofState, isProofState: true };
  if (response instanceof CommandResponse) return { replId: response.env, isProofState: false };
  throw new Error(`Cannot store the session state for unsupported response of type ${response.constructor.name}.`);
}

/** Designates sorry number `index` of the session state `parent`. */
export interface SorryRef {
  parent: number;
  index: number;
}

/** A replay cache entry: the request that produced the state. */
export interface ReplaySessionState extends SessionState {
  /** The request to replay; `null` for a state that is a sorry of another state (see `sorryOf`). */
  request: REPLRequest | null;
  /** Proof states of the sorries of the producing command response, by server id. */
  sorries: Map<string, Array<number | undefined>>;
  /** Set when this state is a sorry of another state; it is materialized with its parent. */
  sorryOf?: SorryRef;
  /** Set when `request` starts from a sorry of another state; substituted on replay. */
  inputSorry?: SorryRef;
}

/** Field holding the env / proof state a request starts from, if any. */
function inputField(request: REPLRequest): { field: "env" | "proofState"; isProofState: boolean } | null {
  switch (requestKind(request)) {
    case "Command":
    case "FileCommand":
    case "UnpickleProofState":
    case "PickleEnvironment":
      return { field: "env", isProofState: false };
    case "ProofStep":
    case "PickleProofState":
      return { field: "proofState", isProofState: true };
    default:
      return null;
  }
}

function withInput(request: REPLRequest, field: "env" | "proofState", value: number): REPLRequest {
  const copy = cloneRequest(request);
  (copy as unknown as Record<string, unknown>)[field] = value;
  return copy;
}

function sorryProofStates(response: BaseREPLResponse): Array<number | undefined> {
  return response instanceof CommandResponse ? response.sorries.map((s) => s.proofState ?? undefined) : [];
}

/**
 * Replays cached requests to restore states after a restart or on another server.
 *
 * @param lazy When `true` (default), states are re-materialized on demand the next time they are
 * used. When `false`, {@link reload} eagerly replays every state.
 */
export class ReplaySessionCache extends BaseSessionCache {
  private readonly cache = new Map<number, ReplaySessionState>();
  private stateCounter = 0;
  /** In-flight materializations, by `sessionId:serverId`. */
  private readonly materializing = new Map<string, Promise<void>>();
  readonly lazy: boolean;

  constructor(options: { lazy?: boolean } = {}) {
    super();
    this.lazy = options.lazy ?? true;
  }

  /** Whether a state is currently materialized on `server` (no replay needed). */
  isMaterialized(sessionId: number, server: CacheServer): boolean {
    const state = this.cache.get(sessionId);
    return state !== undefined && state.replIds.get(server.id) !== undefined;
  }

  private materialize(server: CacheServer, state: ReplaySessionState, options: { timeout?: number; verbose?: boolean } = {}): Promise<void> {
    if (state.replIds.get(server.id) !== undefined) return Promise.resolve();
    // Concurrent requests for the same state share one replay (Python raises instead).
    const key = `${state.sessionId}:${server.id}`;
    const inFlight = this.materializing.get(key);
    if (inFlight) return inFlight;

    const run = (async () => {
      if (state.sorryOf) {
        state.replIds.set(server.id, await this.sorryState(server, state.sorryOf));
        return;
      }
      let request = cloneRequest(state.request!);
      if (state.inputSorry) {
        const input = inputField(request)!;
        request = withInput(request, input.field, await this.sorryState(server, state.inputSorry));
      }
      const response = await server.run(request, options);
      if (response instanceof LeanError) {
        throw new Error(`Could not replay the cached state. The Lean server returned an error: ${response.message}`);
      }
      if (state.isProofState) {
        if (!(response instanceof ProofStepResponse)) {
          throw new Error("Could not replay the cached proof state. The Lean server returned an unexpected response.");
        }
        state.replIds.set(server.id, response.proofState);
      } else {
        if (!(response instanceof CommandResponse)) {
          throw new Error("Could not replay the cached environment. The Lean server returned an unexpected response.");
        }
        state.replIds.set(server.id, response.env);
        state.sorries.set(server.id, sorryProofStates(response));
      }
    })().finally(() => this.materializing.delete(key));
    this.materializing.set(key, run);
    return run;
  }

  /** Materialize `ref.parent` on `server` and return the REPL proof state of the referenced sorry. */
  private async sorryState(server: CacheServer, ref: SorryRef): Promise<number> {
    await this.getReplId(ref.parent, server);
    const ps = this.cache.get(ref.parent)?.sorries.get(server.id)?.[ref.index];
    if (ps === undefined) {
      throw new Error(`Could not replay the cached state: sorry ${ref.index} of session state ${ref.parent} has no proof state.`);
    }
    return ps;
  }

  async add(server: CacheServer, request: REPLRequest, response: BaseREPLResponse): Promise<number> {
    const { replId, isProofState } = stateIdOf(response);
    const sessionId = --this.stateCounter;
    const state: ReplaySessionState = {
      sessionId,
      isProofState,
      request: cloneRequest(request),
      replIds: new Map([[server.id, replId]]),
      sorries: new Map([[server.id, sorryProofStates(response)]]),
    };
    this.resolveInput(state, server.id);
    this.cache.set(sessionId, state);
    return sessionId;
  }

  /**
   * Rewrite a request that starts from a raw REPL id produced by another cached state on server
   * `key`, so that it can be replayed after a restart (where raw ids are meaningless). Upstream
   * LeanInteract stores the raw id and fails to replay such requests.
   */
  private resolveInput(state: ReplaySessionState, key: string): void {
    const input = inputField(state.request!);
    if (!input) return;
    const raw = (state.request as unknown as Record<string, unknown>)[input.field];
    if (typeof raw !== "number" || raw < 0) return;
    for (const other of this.cache.values()) {
      if (other.isProofState === input.isProofState && other.replIds.get(key) === raw) {
        state.request = withInput(state.request!, input.field, other.sessionId);
        return;
      }
    }
    if (!input.isProofState) return;
    for (const other of this.cache.values()) {
      const index = other.sorries.get(key)?.indexOf(raw) ?? -1;
      if (index >= 0) {
        state.inputSorry = { parent: other.sessionId, index };
        return;
      }
    }
  }

  /**
   * Add the proof states of the sorries of the cached environment `parent`, as produced on
   * `server`, and return their session ids (`undefined` for a sorry without proof state).
   * {@link LeanServerPool} uses it so that sorry proof states returned to callers are valid on
   * every worker.
   */
  addSorryStates(parent: number, server: CacheServer): Array<number | undefined> {
    const sorries = this.get(parent).sorries.get(server.id) ?? [];
    return sorries.map((ps, index) => {
      if (ps === undefined) return undefined;
      const sessionId = --this.stateCounter;
      this.cache.set(sessionId, {
        sessionId,
        isProofState: true,
        request: null,
        replIds: new Map([[server.id, ps]]),
        sorries: new Map(),
        sorryOf: { parent, index },
      });
      return sessionId;
    });
  }

  async remove(sessionStateId: number): Promise<void> {
    this.cache.delete(sessionStateId);
  }

  async reload(server: CacheServer, timeoutPerStateMs: number | undefined, options: VerboseOption = {}): Promise<void> {
    const states = [...this.cache.values()];
    for (const state of states) {
      state.replIds.set(server.id, undefined);
      state.sorries.delete(server.id);
    }
    if (this.lazy) return;
    for (const state of states) await this.materialize(server, state, { timeout: timeoutPerStateMs, verbose: options.verbose });
  }

  isEmpty(): boolean {
    return this.cache.size === 0;
  }

  async clear(): Promise<void> {
    this.cache.clear();
  }

  [Symbol.iterator](): Iterator<ReplaySessionState> {
    return [...this.cache.values()][Symbol.iterator]();
  }

  has(sessionId: number): boolean {
    return this.cache.has(sessionId);
  }

  get(sessionId: number): ReplaySessionState {
    const state = this.cache.get(sessionId);
    if (!state) throw new RangeError(`Unknown session state ${sessionId}`);
    return state;
  }

  keys(): number[] {
    return [...this.cache.keys()];
  }

  async getReplId(sessionStateId: number, server: CacheServer): Promise<number | undefined> {
    const state = this.get(sessionStateId);
    if (state.replIds.get(server.id) === undefined) await this.materialize(server, state);
    return state.replIds.get(server.id);
  }
}

/** A pickle cache entry: the file holding the pickled state. */
export interface PickleSessionState extends SessionState {
  pickleFile: string;
}

/**
 * Session cache based on the REPL pickling feature and local files.
 *
 * Pickled Lean states are not fully reliable yet; prefer {@link ReplaySessionCache} unless you
 * need on-disk artifacts.
 */
export class PickleSessionCache extends BaseSessionCache {
  private readonly cache = new Map<number, PickleSessionState>();
  private stateCounter = 0;

  constructor(readonly workingDir: string) {
    super();
  }

  async add(server: CacheServer, request: REPLRequest, response: BaseREPLResponse, options: VerboseOption = {}): Promise<number> {
    const sessionId = --this.stateCounter;
    const hashKey = `request_${requestKind(request)}_${randomUUID()}`;
    const pickleFile = join(
      this.workingDir,
      "session_cache",
      `${createHash("sha256").update(hashKey).digest("hex")}_${process.pid}.olean`,
    );
    await fs.mkdir(join(this.workingDir, "session_cache"), { recursive: true });

    const { replId, isProofState } = stateIdOf(response);
    const pickleRequest = isProofState
      ? pickleProofState({ proofState: replId, pickleTo: pickleFile })
      : pickleEnvironment({ env: replId, pickleTo: pickleFile });

    await withFileLock(
      `${pickleFile}.lock`,
      async () => {
        const res = await server.run(pickleRequest, { verbose: options.verbose });
        if (res instanceof LeanError) {
          throw new Error(`Could not store the result in the session cache. The Lean server returned an error: ${res.message}`);
        }
        this.cache.set(sessionId, { sessionId, pickleFile, isProofState, replIds: new Map([[server.id, replId]]) });
      },
      60_000,
    );
    return sessionId;
  }

  async remove(sessionStateId: number): Promise<void> {
    const state = this.cache.get(sessionStateId);
    this.cache.delete(sessionStateId);
    if (!state) return;
    await withFileLock(
      `${state.pickleFile}.lock`,
      async () => {
        if (existsSync(state.pickleFile)) await fs.rm(state.pickleFile, { force: true });
      },
      60_000,
    );
  }

  async reload(server: CacheServer, timeoutPerStateMs: number | undefined, options: VerboseOption = {}): Promise<void> {
    for (const state of [...this.cache.values()]) {
      await withFileLock(
        `${state.pickleFile}.lock`,
        async () => {
          // Python passes the state's stale proof-state id as `env` here; that is an upstream bug, so no env is sent.
          const cmd = state.isProofState
            ? unpickleProofState({ unpickleProofStateFrom: state.pickleFile })
            : unpickleEnvironment({ unpickleEnvFrom: state.pickleFile });
          const result = await server.run(cmd, { verbose: options.verbose, timeout: timeoutPerStateMs });
          if (result instanceof LeanError) {
            throw new Error(`Could not reload the session cache. The Lean server returned an error: ${result.message}`);
          } else if (result instanceof CommandResponse) {
            state.replIds.set(server.id, result.env);
          } else if (result instanceof ProofStepResponse) {
            state.replIds.set(server.id, result.proofState);
          } else {
            throw new Error(`Could not reload the session cache. The Lean server returned an unexpected response: ${String(result)}`);
          }
        },
        timeoutPerStateMs,
      );
    }
  }

  isEmpty(): boolean {
    return this.cache.size === 0;
  }

  async clear(): Promise<void> {
    for (const id of [...this.cache.keys()]) await this.remove(id);
  }

  [Symbol.iterator](): Iterator<PickleSessionState> {
    return [...this.cache.values()][Symbol.iterator]();
  }

  has(sessionId: number): boolean {
    return this.cache.has(sessionId);
  }

  get(sessionId: number): PickleSessionState {
    const state = this.cache.get(sessionId);
    if (!state) throw new RangeError(`Unknown session state ${sessionId}`);
    return state;
  }

  keys(): number[] {
    return [...this.cache.keys()];
  }

  async getReplId(sessionStateId: number, server: CacheServer): Promise<number | undefined> {
    return this.get(sessionStateId).replIds.get(server.id);
  }
}
