/** Subprocess helpers. */

import { spawn } from "node:child_process";

export interface ExecResult {
  code: number;
  stdout: string;
  stderr: string;
}

export interface ExecOptions {
  cwd?: string;
  /** Stream output to this process's stdio instead of capturing it. */
  inherit?: boolean;
  shell?: boolean;
}

/** Thrown when the executable cannot be found. */
export class ExecutableNotFoundError extends Error {
  constructor(
    readonly file: string,
    cause?: unknown,
  ) {
    super(`Executable not found: ${file}`, { cause });
    this.name = "ExecutableNotFoundError";
  }
}

/** Run a command to completion. Never rejects on non-zero exit; rejects if it cannot start. */
export function exec(file: string, args: string[], options: ExecOptions = {}): Promise<ExecResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, {
      cwd: options.cwd,
      shell: options.shell,
      stdio: options.inherit ? ["ignore", "inherit", "inherit"] : ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    child.stdout?.setEncoding("utf8").on("data", (d: string) => (stdout += d));
    child.stderr?.setEncoding("utf8").on("data", (d: string) => (stderr += d));
    child.once("error", (err: NodeJS.ErrnoException) => {
      reject(err.code === "ENOENT" ? new ExecutableNotFoundError(file, err) : err);
    });
    child.once("close", (code, signal) => resolve({ code: code ?? (signal ? 128 : 1), stdout, stderr }));
  });
}

/** Run a command and throw if it exits with a non-zero code. */
export async function execChecked(file: string, args: string[], options: ExecOptions = {}): Promise<ExecResult> {
  const res = await exec(file, args, options);
  if (res.code !== 0) {
    const err = new Error(
      `Command \`${[file, ...args].join(" ")}\` failed with exit code ${res.code}` +
        (options.inherit ? "" : `\nstdout:\n${res.stdout}\nstderr:\n${res.stderr}`),
    );
    Object.assign(err, { exitCode: res.code, stdout: res.stdout, stderr: res.stderr });
    throw err;
  }
  return res;
}
