/** Minimal leveled logger, replaceable with {@link setLogger}. */

export type LogLevel = "debug" | "info" | "warn" | "error" | "silent";

export interface Logger {
  debug(message: string, ...args: unknown[]): void;
  info(message: string, ...args: unknown[]): void;
  warn(message: string, ...args: unknown[]): void;
  error(message: string, ...args: unknown[]): void;
}

const ORDER: Record<LogLevel, number> = { debug: 10, info: 20, warn: 30, error: 40, silent: 100 };

let level: LogLevel = (process.env.LEAN_INTERACT_LOG_LEVEL as LogLevel | undefined) ?? "info";

function emit(lvl: Exclude<LogLevel, "silent">, message: string, args: unknown[]): void {
  if (ORDER[lvl] < ORDER[level]) return;
  const time = new Date().toTimeString().slice(0, 8);
  console.error(`[${time}] ${lvl.toUpperCase().padEnd(5)} lean_interact: ${message}`, ...args);
}

const defaultLogger: Logger = {
  debug: (m, ...a) => emit("debug", m, a),
  info: (m, ...a) => emit("info", m, a),
  warn: (m, ...a) => emit("warn", m, a),
  error: (m, ...a) => emit("error", m, a),
};

/** The library logger. Writes to stderr; level from `LEAN_INTERACT_LOG_LEVEL` (default `info`). */
export const logger: Logger = {
  debug: (m, ...a) => current.debug(m, ...a),
  info: (m, ...a) => current.info(m, ...a),
  warn: (m, ...a) => current.warn(m, ...a),
  error: (m, ...a) => current.error(m, ...a),
};

let current: Logger = defaultLogger;

/** Set the minimum level of the default logger. */
export function setLogLevel(newLevel: LogLevel): void {
  level = newLevel;
}

/** Route library logs to a custom logger (e.g. pino, winston). Pass `null` to restore the default. */
export function setLogger(custom: Logger | null): void {
  current = custom ?? defaultLogger;
}
