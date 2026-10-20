/**
 * LeanInteract for TypeScript: interact with the Lean 4 theorem prover through the Lean REPL.
 *
 * @example
 * ```ts
 * import { LeanREPLConfig, LeanServer, command } from "lean-interact";
 *
 * const config = await LeanREPLConfig.create({ verbose: true });
 * await using server = new LeanServer(config);
 * console.log(await server.run(command({ cmd: "theorem ex (n : Nat) : n = 5 → n = 5 := id" })));
 * ```
 *
 * @module
 */

export * from "./interface.js";
export { LeanREPLConfig, type LeanREPLConfigOptions } from "./config.js";
export {
  BaseProject,
  BaseTempProject,
  GitProject,
  LeanRequire,
  LocalProject,
  TemporaryProject,
  TempRequireProject,
  type BaseProjectOptions,
  type BaseTempProjectOptions,
  type BuildOptions,
  type GitProjectOptions,
  type Requirement,
  type TemporaryProjectOptions,
  type TempRequireProjectOptions,
} from "./project.js";
export {
  AutoLeanServer,
  LeanMemoryError,
  LeanOutputParseError,
  LeanServer,
  LeanServerClosedError,
  LeanServerNotRunningError,
  LeanTimeoutError,
  type AutoLeanServerOptions,
  type AutoRunOptions,
  type RunOptions,
} from "./server.js";
export {
  BaseSessionCache,
  PickleSessionCache,
  ReplaySessionCache,
  type CacheServer,
  type PickleSessionState,
  type ReplaySessionState,
  type SessionState,
} from "./sessioncache.js";
export { LeanServerPool, type LeanServerPoolOptions, type RunBatchOptions } from "./pool.js";
export * from "./utils.js";
export { FileLock, LockTimeoutError, withFileLock } from "./internal/filelock.js";
export { logger, setLogger, setLogLevel, type Logger, type LogLevel } from "./internal/log.js";
