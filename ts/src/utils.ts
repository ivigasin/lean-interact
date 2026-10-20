/**
 * Utilities: Lean code string manipulation, Lean version helpers, lake checks, Lean installation.
 *
 * Port of `lean_interact.utils`. String functions return UTF-16 indices, consistent with
 * JavaScript string slicing.
 *
 * @module
 */

import { existsSync, readFileSync, statSync, promises as fs } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { exec, ExecutableNotFoundError } from "./internal/exec.js";
import { logger } from "./internal/log.js";
import { defaultCacheDir } from "./internal/paths.js";

export { DEFAULT_REPL_GIT_URL, DEFAULT_REPL_VERSION, defaultCacheDir } from "./internal/paths.js";
export { getTotalMemoryUsage, getSystemMemoryPercent } from "./internal/proc.js";
export { compareLeanVersions } from "./internal/version.js";

const GET_STARTED_URL = "https://leanprover-community.github.io/get_started.html";

/** Delete the LeanInteract cache directory. */
export async function clearCache(cacheDir: string = defaultCacheDir()): Promise<void> {
  await fs.rm(cacheDir, { recursive: true, force: true });
}

/** Normalize a Lean version (`leanprover/lean4:v4.19.0` → `v4.19.0`). Throws if it is not a `v4` version. */
export function parseLeanVersion(leanVersion: string): string {
  const v = leanVersion.startsWith("leanprover/lean4:") ? leanVersion.slice("leanprover/lean4:".length) : leanVersion;
  if (!v.startsWith("v4")) throw new Error("Unable to parse Lean version format!");
  return v;
}

/** Lean version declared in `<projectDir>/lean-toolchain`, or `null`. */
export function getProjectLeanVersion(projectDir: string): string | null {
  const file = join(projectDir, "lean-toolchain");
  try {
    if (!statSync(file).isFile()) return null;
    return parseLeanVersion(readFileSync(file, "utf8").trim());
  } catch {
    return null;
  }
}

/** Check that `lake` can be executed. Throws a descriptive error otherwise. */
export async function checkLake(lakePath: string = "lake", verbose = false): Promise<void> {
  let res;
  try {
    res = await exec(lakePath, ["--version"], { inherit: verbose });
  } catch (err) {
    if (err instanceof ExecutableNotFoundError) {
      throw new Error(
        `Lean 4 build system executable not found at \`${lakePath}\`. ` +
          `You can try to run \`lean-interact install-lean\` or follow: ${GET_STARTED_URL}`,
        { cause: err },
      );
    }
    throw err;
  }
  if (res.code !== 0) {
    throw new Error(
      `Lean 4 build system (\`${lakePath}\`) failed with exit code ${res.code}.\nstdout:\n${res.stdout}\n\nstderr:\n${res.stderr}`,
    );
  }
}

/**
 * Install the Lean version manager (elan) with the official installer script, and add
 * `~/.elan/bin` to `PATH` in existing shell config files.
 */
export async function installLean(): Promise<void> {
  const os = process.platform;
  logger.info("Detected operating system: %s", os);
  const run = async (command: string) => {
    const res = await exec(command, [], { shell: true, inherit: true });
    if (res.code !== 0) throw new Error(`Command failed (${res.code}): ${command}`);
  };
  try {
    if (os === "win32") {
      logger.info("Installing elan for Windows...");
      await run("curl -O --location https://raw.githubusercontent.com/leanprover/elan/master/elan-init.ps1");
      await run(
        "powershell -ExecutionPolicy Bypass -Command \"& './elan-init.ps1' -NoPrompt $true -DefaultToolchain stable\"",
      );
      await run("del elan-init.ps1");
      logger.info("Elan has been installed. You may need to restart your terminal for the PATH changes to take effect.");
    } else if (os === "linux" || os === "darwin") {
      await run(
        "curl https://raw.githubusercontent.com/leanprover/elan/master/elan-init.sh -sSf | sh -s -- -y --default-toolchain stable",
      );
      for (const name of [".bashrc", ".zshrc", ".bash_profile", ".profile"]) {
        const path = join(homedir(), name);
        if (!existsSync(path)) continue;
        try {
          await fs.appendFile(path, '\nexport PATH="$HOME/.elan/bin:$PATH"\n', "utf8");
          logger.info("Added elan to PATH in %s", path);
        } catch (err) {
          logger.warn("Could not modify %s: %s", path, String(err));
        }
      }
      logger.info("Please restart your terminal or run 'source ~/.profile' to update your PATH");
    } else {
      throw new Error(`Unsupported operating system: ${os}. Please install elan manually: ${GET_STARTED_URL}`);
    }
    logger.info("Lean installation completed successfully.");
  } catch (err) {
    logger.warn("An error occurred during Lean installation: %s\nPlease check %s for more information.", String(err), GET_STARTED_URL);
    throw err;
  }
}

// ---------------------------------------------------------------------------
// Lean code string utilities
// ---------------------------------------------------------------------------

/** Indent every line of `code` by `nbSpaces` spaces. */
export function indentCode(code: string, nbSpaces = 2): string {
  const pad = " ".repeat(nbSpaces);
  return code
    .split("\n")
    .map((line) => pad + line)
    .join("\n");
}

/** Blank out whitespace-only lines and collapse runs of blank lines. */
export function compressNewlines(leanCode: string): string {
  let code = leanCode.replace(/^\s+$/gm, "");
  code = code.replace(/\n\n+/g, "\n\n");
  code = code.trimStart();
  if (code.endsWith("\n")) code = code.trimEnd() + "\n";
  return code;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Ranges `[start, end)` of Lean comments in a code snippet, merged when overlapping. Handles a
 * multiline comment left open at the end or opened before the start of the snippet.
 *
 * @param multilineCommentSuffix Regex fragment after `/-` / before `-/` (e.g. `-` for docstrings).
 */
export function leanCommentsRanges(
  leanCode: string,
  multilineCommentSuffix = "",
  removeSingleLineComments = true,
): Array<[number, number]> {
  const openIdx = [...leanCode.matchAll(new RegExp("/-" + multilineCommentSuffix, "g"))].map((m) => m.index!);
  const closeIdx = [...leanCode.matchAll(new RegExp(multilineCommentSuffix + "-/", "g"))].map(
    (m) => m.index! + multilineCommentSuffix.length + 2,
  );

  if (openIdx.length === closeIdx.length + 1) {
    closeIdx.push(leanCode.length); // last comment not closed (partial code)
  } else if (openIdx.length + 1 === closeIdx.length) {
    openIdx.unshift(0); // first comment opened before the snippet
  } else if (openIdx.length !== closeIdx.length) {
    throw new Error("Mismatched open and close comment indices.");
  }

  const ranges: Array<[number, number]> = openIdx.map((o, i) => [o, closeIdx[i]]);

  if (removeSingleLineComments) {
    for (const m of leanCode.matchAll(/--/g)) {
      const nl = leanCode.indexOf("\n", m.index!);
      ranges.push([m.index!, nl === -1 ? leanCode.length : nl]);
    }
  }

  ranges.sort((a, b) => a[0] - b[0]);
  const merged: Array<[number, number]> = [];
  for (const [start, end] of ranges) {
    const last = merged[merged.length - 1];
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }
  return merged;
}

/** Remove all comments from Lean code. Returns `null` if comments are unbalanced. */
export function removeLeanComments(leanCode: string): string | null {
  try {
    let out = "";
    let prev = 0;
    for (const [start, end] of leanCommentsRanges(leanCode)) {
      out += leanCode.slice(prev, start);
      prev = end;
    }
    return out + leanCode.slice(prev);
  } catch {
    return null;
  }
}

const OPEN = new Set(["(", "{", "["]);
const CLOSING: Record<string, string> = { ")": "(", "}": "{", "]": "[" };

/**
 * Index of the `:=` separating a declaration from its implementation (proof), ignoring `:=` nested
 * in brackets or belonging to `let` / `haveI`. `null` if none.
 */
export function splitImplementation(declaration: string, start = 0): number | null {
  if (!declaration.includes(":=")) return null;
  const indices = new Set([...declaration.matchAll(/:=/g)].map((m) => m.index!));
  for (const keyword of ["let", "haveI"]) {
    for (const m of declaration.matchAll(new RegExp(`${escapeRegExp(keyword)}\\s+\\S*?\\s*(:=)`, "dg"))) {
      indices.delete(m.indices![1]![0]);
    }
  }
  const counters: Record<string, number> = { "(": 0, "{": 0, "[": 0 };
  for (let i = start; i < declaration.length; i++) {
    const c = declaration[i];
    if (OPEN.has(c)) counters[c]++;
    else if (c in CLOSING) counters[CLOSING[c]]--;
    if (counters["("] === 0 && counters["{"] === 0 && counters["["] === 0 && indices.has(i)) return i;
  }
  return null;
}

/** Index of the top-level `:` separating binders from the conclusion. `null` if none. */
export function splitConclusion(declaration: string, start = 0): number | null {
  const counters: Record<string, number> = { "(": 0, "{": 0, "[": 0 };
  for (let i = start; i < declaration.length; i++) {
    const c = declaration[i];
    if (OPEN.has(c)) counters[c]++;
    else if (c in CLOSING) counters[CLOSING[c]]--;
    if (counters["("] === 0 && counters["{"] === 0 && counters["["] === 0 && c === ":") return i;
  }
  return null;
}

/**
 * Normalize a theorem: strip comments and proof, rename it to `newTheoremName`, optionally append
 * `:= by sorry`. Assumes no other declaration is present. Returns `null` on failure.
 */
export function cleanTheoremString(theoremString: string, newTheoremName = "dummy", addSorry = true): string | null {
  try {
    let clean = removeLeanComments(theoremString);
    if (clean === null) throw new Error("Comment removal failed.");
    clean = clean.trim();

    // Same (precedence-quirky) pattern as Python: `\btheorem|lemma|example\s`.
    const m = /\btheorem|lemma|example\s/.exec(clean);
    if (m === null) throw new Error("Theorem declaration keyword not found.");
    clean = clean.slice(m.index);

    const idxImplement = splitImplementation(clean);
    if (idxImplement !== null) clean = clean.slice(0, idxImplement).trim();

    if (clean.trim().startsWith("example")) {
      clean = clean.replace(/^[^\s]+/, "").trim();
    } else {
      clean = clean.replace(/^[^\s]+/, "").trim();
      clean = clean.replace(/^[^\s:({\[]+/, "").trim();
    }
    clean = `theorem ${newTheoremName} ` + clean;
    if (addSorry) clean += " := by sorry";
    return clean;
  } catch {
    return null;
  }
}

/** Start index of the last `theorem` / `lemma` / `example` outside comments. Throws if none. */
export function extractLastTheorem(leanCode: string): number {
  const commentRanges = leanCommentsRanges(leanCode);
  let indices: number[] = [];
  for (const keyword of ["theorem", "lemma", "example"]) {
    indices.push(...[...leanCode.matchAll(new RegExp(`\\b${keyword}\\s`, "g"))].map((m) => m.index!));
  }
  indices = indices.filter((idx) => !commentRanges.some(([s, e]) => s <= idx && idx <= e));
  if (indices.length === 0) throw new Error(`No theorem found in the provided Lean code:\n${leanCode}`);
  return indices[indices.length - 1];
}

/** {@link cleanTheoremString} applied to the last theorem of a snippet, keeping the preceding code. */
export function cleanLastTheoremString(leanCode: string, newTheoremName = "dummy", addSorry = false): string {
  const idx = extractLastTheorem(leanCode);
  const clean = cleanTheoremString(leanCode.slice(idx), newTheoremName, addSorry);
  if (clean !== null) return leanCode.slice(0, idx) + clean;
  throw new Error(`Theorem extraction failed for the following Lean code:\n${leanCode}`);
}
