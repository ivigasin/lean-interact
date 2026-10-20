import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { LeanREPLConfig, setLogLevel, type LeanREPLConfigOptions } from "../src/index.js";

setLogLevel("silent");

export const FIXTURES = resolve(dirname(fileURLToPath(import.meta.url)), "../../test/fixtures");

/** A temp dir with a fake `lake` and a fake REPL checkout, and a config using them. */
export async function fakeConfig(options: Partial<LeanREPLConfigOptions> = {}): Promise<{ config: LeanREPLConfig; dir: string }> {
  const dir = mkdtempSync(join(tmpdir(), "lean-interact-test-"));
  const replDir = join(dir, "repl");
  const binDir = join(replDir, ".lake", "build", "bin");
  mkdirSync(binDir, { recursive: true });
  writeFileSync(join(replDir, "lean-toolchain"), "leanprover/lean4:v4.19.0\n");
  copyFileSync(join(FIXTURES, "fake_repl.mjs"), join(binDir, "repl"));
  chmodSync(join(binDir, "repl"), 0o755);

  const lake = join(dir, "lake");
  writeFileSync(
    lake,
    '#!/bin/sh\nif [ "$1" = "--version" ]; then echo "Lake version 0.0.0-fake"; exit 0; fi\n' +
      'if [ "$1" = "env" ]; then shift; exec "$@"; fi\nexit 0\n',
  );
  chmodSync(lake, 0o755);

  const config = await LeanREPLConfig.create({ localReplPath: replDir, buildRepl: false, lakePath: lake, ...options });
  return { config, dir };
}
