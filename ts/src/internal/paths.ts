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

/** `owner/repo` path components of a git URL (`.git` stripped), mirroring the Python logic. */
export function repoPathParts(url: string): string[] {
  const parts = url.split("/");
  if (parts.length >= 2) return [parts[parts.length - 2], parts[parts.length - 1].replace(".git", "")];
  return [url.replace(".git", "")];
}
