// Runs the compiled tests. `node --test` only expands globs from Node 21, and npm scripts cannot
// set environment variables portably, so this lists the files itself.
//   node scripts/run-tests.mjs                 all tests (end-to-end ones skip without Lean)
//   node scripts/run-tests.mjs --e2e           only the real-Lean end-to-end tests
//   node scripts/run-tests.mjs <file>...       the given compiled test files
import { spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";

const dir = "build-test/test";
const args = process.argv.slice(2);
const e2e = args.includes("--e2e");
const named = args.filter((a) => !a.startsWith("--"));
const env = { ...process.env, ...(e2e ? { LEAN_INTERACT_E2E: "1" } : {}) };

const files = named.length
  ? named
  : readdirSync(dir)
      .filter((f) => (e2e ? f === "e2e.test.js" : f.endsWith(".test.js")))
      .map((f) => `${dir}/${f}`);

const res = spawnSync(process.execPath, ["--test", "--test-concurrency=1", ...files], { stdio: "inherit", env });
process.exit(res.status ?? 1);
