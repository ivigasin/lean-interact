/** Default locations. */

import { homedir } from "node:os";
import { join } from "node:path";

export const DEFAULT_REPL_GIT_URL = "https://github.com/augustepoiroux/repl";
export const DEFAULT_REPL_VERSION = "v1.3.19";

/**
 * Default cache directory: `$LEAN_INTERACT_CACHE_DIR`, else `<user cache dir>/lean_interact`
 * (`~/Library/Caches` on macOS, `$XDG_CACHE_HOME` or `~/.cache` on Linux, `%LocalAppData%` on Windows).
 */
export function defaultCacheDir(): string {
  const env = process.env.LEAN_INTERACT_CACHE_DIR;
  if (env) return env;
  const home = homedir();
  let base: string;
  if (process.platform === "darwin") base = join(home, "Library", "Caches");
  else if (process.platform === "win32") base = process.env.LOCALAPPDATA || join(home, "AppData", "Local");
  else base = process.env.XDG_CACHE_HOME || join(home, ".cache");
  return join(base, "lean_interact");
}

/**
 * `owner/repo` path components of a git URL or local path (`.git` stripped), based on the Python
 * logic. Both slash kinds separate components, and characters that are invalid in Windows file
 * names (such as the `:` of `C:` or `git@host:owner`) become `_`; Python splits on `/` only and
 * fails on both.
 */
export function repoPathParts(url: string): string[] {
  const clean = (s: string) => s.replaceAll(".git", "").replace(/[:*?"<>|]/g, "_");
  const parts = url.split(/[/\\]/).filter((p) => p !== "");
  if (parts.length >= 2) return [clean(parts[parts.length - 2]!), clean(parts[parts.length - 1]!)];
  return [clean(parts[0] ?? url)];
}
