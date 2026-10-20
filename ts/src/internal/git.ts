/** Thin wrapper around the `git` CLI (replacement for GitPython's `_GitUtilities`). */

import { exec, execChecked } from "./exec.js";
import { logger } from "./log.js";

/** Clone a repository into `directory` (which may exist and be empty). */
export async function gitClone(url: string, directory: string): Promise<void> {
  await execChecked("git", ["clone", url, directory]);
}

/** Git operations on a repository that report failure as `false` instead of throwing. */
export class GitRepo {
  constructor(readonly path: string) {}

  private git(...args: string[]) {
    return exec("git", args, { cwd: this.path });
  }

  async safeCheckout(revision: string): Promise<boolean> {
    const res = await this.git("checkout", revision);
    if (res.code === 0) {
      logger.debug("Successfully checked out revision: %s", revision);
      return true;
    }
    logger.debug("Failed to checkout revision '%s': %s", revision, res.stderr.trim());
    return false;
  }

  async safeFetch(remote = "origin"): Promise<boolean> {
    const res = await this.git("fetch", "--tags", remote);
    if (res.code === 0) {
      logger.debug("Successfully fetched from remote: %s", remote);
      return true;
    }
    logger.warn("Failed to fetch from remote '%s': %s", remote, res.stderr.trim());
    return false;
  }

  async safePull(remote = "origin"): Promise<boolean> {
    const res = await this.git("pull", remote);
    if (res.code === 0) {
      logger.debug("Successfully pulled from remote: %s", remote);
      return true;
    }
    logger.warn("Failed to pull from remote '%s': %s", remote, res.stderr.trim());
    return false;
  }

  async safeResetHard(target: string): Promise<boolean> {
    const res = await this.git("reset", "--hard", target);
    if (res.code === 0) {
      logger.debug("Successfully reset to: %s", target);
      return true;
    }
    logger.warn("Failed to reset to '%s': %s", target, res.stderr.trim());
    return false;
  }

  async updateSubmodules(): Promise<boolean> {
    const res = await this.git("submodule", "update", "--init", "--recursive");
    if (res.code === 0) {
      logger.debug("Successfully updated submodules");
      return true;
    }
    logger.warn("Failed to update submodules: %s", res.stderr.trim());
    return false;
  }

  /** Current branch name, or `null` on a detached HEAD. */
  async getCurrentBranchName(): Promise<string | null> {
    const res = await this.git("symbolic-ref", "--short", "-q", "HEAD");
    return res.code === 0 && res.stdout.trim() ? res.stdout.trim() : null;
  }

  async branchExistsLocally(branch: string): Promise<boolean> {
    return (await this.git("show-ref", "--verify", "--quiet", `refs/heads/${branch}`)).code === 0;
  }

  /** Whether a remote-tracking ref such as `origin/main` exists. */
  async remoteRefExists(refName: string): Promise<boolean> {
    return (await this.git("show-ref", "--verify", "--quiet", `refs/remotes/${refName}`)).code === 0;
  }

  async headCommit(): Promise<string> {
    return (await execChecked("git", ["rev-parse", "HEAD"], { cwd: this.path })).stdout.trim();
  }

  async tags(): Promise<string[]> {
    const res = await this.git("tag", "--list");
    return res.code === 0 ? res.stdout.split("\n").map((t) => t.trim()).filter(Boolean) : [];
  }
}
