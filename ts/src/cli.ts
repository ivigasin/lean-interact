#!/usr/bin/env node
/**
 * `lean-interact` command line: `install-lean` and `clear-cache`.
 * Also callable as `install-lean` / `clear-lean-cache` (Python script names).
 */

import { basename } from "node:path";

import { clearCache, defaultCacheDir, installLean } from "./utils.js";

const USAGE = `Usage: lean-interact <command>

Commands:
  install-lean            Install the Lean version manager (elan)
  clear-cache [--cache-dir <dir>]
                          Delete the LeanInteract cache (default: ${defaultCacheDir()})
`;

async function main(argv: string[]): Promise<number> {
  const invokedAs = basename(process.argv[1] ?? "").replace(/\.(c|m)?js$/, "");
  let [cmd, ...rest] = argv;
  if (invokedAs === "install-lean") [cmd, rest] = ["install-lean", argv];
  else if (invokedAs === "clear-lean-cache") [cmd, rest] = ["clear-cache", argv];

  switch (cmd) {
    case "install-lean":
      await installLean();
      return 0;
    case "clear-cache":
    case "clear-lean-cache": {
      const i = rest.indexOf("--cache-dir");
      const dir = i !== -1 && rest[i + 1] ? rest[i + 1] : defaultCacheDir();
      await clearCache(dir);
      console.log(`Cleared ${dir}`);
      return 0;
    }
    case "-h":
    case "--help":
    case "help":
      process.stdout.write(USAGE);
      return 0;
    default:
      process.stderr.write(USAGE);
      return 2;
  }
}

main(process.argv.slice(2)).then(
  (code) => process.exit(code),
  (err) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  },
);
