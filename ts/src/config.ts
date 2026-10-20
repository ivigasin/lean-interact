/**
 * Configuration of the Lean REPL used by the Lean servers.
 *
 * Port of `lean_interact.config`. The Python constructor performs the setup (clone, checkout,
 * build); here {@link LeanREPLConfig.create} does it asynchronously.
 *
 * @module
 */

import { existsSync, promises as fs } from "node:fs";
import { join, resolve } from "node:path";

import { exec, ExecutableNotFoundError } from "./internal/exec.js";
import { withFileLock } from "./internal/filelock.js";
import { gitClone, GitRepo } from "./internal/git.js";
import { logger } from "./internal/log.js";
import { DEFAULT_REPL_GIT_URL, DEFAULT_REPL_VERSION, defaultCacheDir, repoPathParts } from "./internal/paths.js";
import { compareLeanVersions } from "./internal/version.js";
import type { BaseProject } from "./project.js";
import { checkLake, getProjectLeanVersion, parseLeanVersion } from "./utils.js";

const LOCK_TIMEOUT_MS = 300_000;

export interface LeanREPLConfigOptions {
  /**
   * Lean version to use (e.g. `"v4.19.0"`). Only when `project` is not set; with a project the
   * version is inferred from it. Default: the latest version available for the REPL.
   */
  leanVersion?: string;
  /** Project the REPL runs in. Default: none (Lean + standard library only). */
  project?: BaseProject;
  /** REPL git revision. `<replRev>_lean-toolchain-<leanVersion>` is tried first. Ignored with `localReplPath`. */
  replRev?: string;
  /** REPL git repository. Ignored with `localReplPath`. */
  replGit?: string;
  /** Always fetch the REPL repository before checking out. Default `false` (limits GitHub rate limiting). */
  forcePullRepl?: boolean;
  /** Cache directory for REPL checkouts. Default: {@link defaultCacheDir}. */
  cacheDir?: string;
  /** Use a local REPL checkout instead of the git repository. */
  localReplPath?: string;
  /** Build the REPL with `lake build`. Default `true`. */
  buildRepl?: boolean;
  /** Path to the lake executable. Default `"lake"`. */
  lakePath?: string;
  /** Hard memory limit (MB) for the REPL process. Linux only. Default: none. */
  memoryHardLimitMb?: number | null;
  /** Send `incrementality: true` with commands (Lean >= v4.8.0-rc1). Default `true`. */
  enableIncrementalOptimization?: boolean;
  /** Set `Elab.async` for commands (Lean >= v4.19.0). Default `true`. */
  enableParallelElaboration?: boolean;
  /** Print additional information during setup. Default `false`. */
  verbose?: boolean;
}

/** Configuration of the Lean REPL. Create with {@link LeanREPLConfig.create}. */
export class LeanREPLConfig {
  leanVersion: string | null;
  readonly project: BaseProject | null;
  readonly replRev: string;
  readonly replGit: string;
  readonly forcePullRepl: boolean;
  readonly cacheDir: string;
  readonly localReplPath: string | null;
  readonly buildRepl: boolean;
  readonly lakePath: string;
  readonly memoryHardLimitMb: number | null;
  readonly enableIncrementalOptimization: boolean;
  readonly enableParallelElaboration: boolean;
  readonly verbose: boolean;

  private _cacheReplDir: string | null = null;
  private repoName: string[] = [];
  private cacheCleanReplDir = "";

  private constructor(options: LeanREPLConfigOptions) {
    if (options.project && options.leanVersion) {
      throw new Error(
        "leanVersion should only be set when project is not set. When a project is provided, the Lean version is inferred from the project.",
      );
    }
    this.leanVersion = options.leanVersion ? parseLeanVersion(options.leanVersion) : null;
    this.project = options.project ?? null;
    this.replRev = options.replRev ?? DEFAULT_REPL_VERSION;
    this.replGit = options.replGit ?? DEFAULT_REPL_GIT_URL;
    this.forcePullRepl = options.forcePullRepl ?? false;
    this.cacheDir = resolve(options.cacheDir ?? defaultCacheDir());
    this.localReplPath = options.localReplPath ? resolve(options.localReplPath) : null;
    this.buildRepl = options.buildRepl ?? true;
    this.lakePath = options.lakePath ?? "lake";
    this.memoryHardLimitMb = options.memoryHardLimitMb ?? null;
    this.enableIncrementalOptimization = options.enableIncrementalOptimization ?? true;
    this.enableParallelElaboration = options.enableParallelElaboration ?? true;
    this.verbose = options.verbose ?? false;

    if (this.project) {
      this.leanVersion = this.project.getLeanVersion();
      if (this.project.directory === null) throw new Error("Project directory cannot be null");
    }
  }

  /**
   * Create a configuration and set up the REPL (clone / checkout / build as needed).
   *
   * @example
   * ```ts
   * const config = await LeanREPLConfig.create({ verbose: true });
   * const pinned = await LeanREPLConfig.create({ leanVersion: "v4.19.0" });
   * const withProject = await LeanREPLConfig.create({ project: await LocalProject.create({ directory: "/path" }) });
   * ```
   */
  static async create(options: LeanREPLConfigOptions = {}): Promise<LeanREPLConfig> {
    const config = new LeanREPLConfig(options);
    await config.setupRepl();
    return config;
  }

  private tagName(leanVersion: string | null): string {
    return `${this.replRev}_lean-toolchain-${leanVersion}`;
  }

  private async setupRepl(): Promise<void> {
    if (this.localReplPath) this.prepareLocalRepl();
    else await this.prepareGitRepl();
    if (this.buildRepl) await this.buildReplNow();
  }

  private prepareLocalRepl(): void {
    const path = this.localReplPath!;
    if (!existsSync(path)) throw new Error(`Local REPL path '${path}' does not exist`);
    const localVersion = getProjectLeanVersion(path);
    if (!localVersion) {
      logger.warn("Could not determine Lean version from local REPL at '%s'", path);
    } else if (this.leanVersion !== null && this.leanVersion !== localVersion) {
      logger.warn("Requested Lean version '%s' does not match version in local REPL '%s'.", this.leanVersion, localVersion);
    }
    if (this.leanVersion === null) this.leanVersion = localVersion;
    this._cacheReplDir = path;
    if (this.verbose) logger.info("Using local REPL at %s", path);
  }

  private async prepareGitRepl(): Promise<void> {
    this.repoName = repoPathParts(this.replGit);
    this.cacheCleanReplDir = join(this.cacheDir, ...this.repoName, "repl_clean_copy");
    await fs.mkdir(join(this.cacheDir, ...this.repoName), { recursive: true });

    await withFileLock(
      `${this.cacheCleanReplDir}.lock`,
      async () => {
        await this.setupCleanReplRepo();
        const git = new GitRepo(this.cacheCleanReplDir);
        if (this.forcePullRepl) await this.forceUpdateRepl(git);
        let ok = await this.checkoutReplRevision(git);
        if (!ok && !this.forcePullRepl) ok = await this.retryCheckoutAfterPull(git);
        await this.validateAndSetLeanVersion();
        await this.setupVersionSpecificReplDir();
      },
      LOCK_TIMEOUT_MS,
    );
  }

  private async setupCleanReplRepo(): Promise<void> {
    if (existsSync(this.cacheCleanReplDir)) return;
    await fs.mkdir(this.cacheCleanReplDir, { recursive: true });
    try {
      await gitClone(this.replGit, this.cacheCleanReplDir);
      logger.debug("Successfully cloned REPL repository from %s", this.replGit);
    } catch (err) {
      logger.error("Failed to clone REPL repository from %s: %s", this.replGit, String(err));
      // Do not leave an empty directory that would be mistaken for a clone next time.
      await fs.rm(this.cacheCleanReplDir, { recursive: true, force: true });
      throw err;
    }
  }

  private async forceUpdateRepl(git: GitRepo): Promise<void> {
    if (!(await git.safeFetch())) {
      logger.warn("Failed to fetch during force update");
      return;
    }
    logger.debug("Force update: successfully fetched latest changes from remote");
    if (this.leanVersion !== null) {
      const targetTag = this.tagName(this.leanVersion);
      if (await git.safeCheckout(targetTag)) {
        logger.debug("Force update: checked out target tag %s", targetTag);
        return;
      }
    }
    const currentBranch = await git.getCurrentBranchName();
    let targetBranch: string | null = null;
    if (currentBranch && (await git.remoteRefExists(`origin/${currentBranch}`))) targetBranch = currentBranch;
    else if (!this.leanVersion) targetBranch = currentBranch;

    if (targetBranch && (await git.remoteRefExists(`origin/${targetBranch}`))) {
      if (await git.safeResetHard(`origin/${targetBranch}`)) {
        logger.debug("Force updated REPL to match remote branch %s", targetBranch);
      } else {
        logger.warn("Failed to reset REPL to remote branch %s", targetBranch);
      }
    } else {
      logger.debug("Force update: fetched all refs, but no matching remote branch for reset");
    }
  }

  private async checkoutReplRevision(git: GitRepo): Promise<boolean> {
    if (this.leanVersion !== null) {
      const tag = this.tagName(this.leanVersion);
      const ok = await git.safeCheckout(tag);
      if (ok) logger.debug("Successfully checked out tag: %s", tag);
      return ok;
    }
    const ok = await git.safeCheckout(this.replRev);
    if (ok) logger.debug("Successfully checked out revision: %s", this.replRev);
    return ok;
  }

  private async retryCheckoutAfterPull(git: GitRepo): Promise<boolean> {
    if (!this.forcePullRepl) {
      if (await git.safePull()) logger.debug("Pulled latest changes to retry checkout");
      else logger.warn("Failed to pull REPL repository, continuing with current state");
    }
    let ok = false;
    if (this.leanVersion !== null) ok = await git.safeCheckout(this.tagName(this.leanVersion));
    if (!ok) {
      if (!(await git.safeCheckout(this.replRev))) throw new Error(`Lean REPL version \`${this.replRev}\` is not available.`);
      ok = true;
    }
    return ok;
  }

  private async validateAndSetLeanVersion(): Promise<void> {
    if (this.leanVersion === null) {
      this._cacheReplDir = this.cacheCleanReplDir;
      const available = await this.availableLeanVersions();
      if (available.length > 0) {
        this.leanVersion = available[available.length - 1][0];
        await new GitRepo(this.cacheCleanReplDir).safeCheckout(this.tagName(this.leanVersion));
      }
    }
    const replLeanVersion = getProjectLeanVersion(this.cacheCleanReplDir);
    if (!this.leanVersion) this.leanVersion = replLeanVersion;
    if (!replLeanVersion || this.leanVersion !== replLeanVersion) {
      throw new Error(
        `An error occurred while preparing the Lean REPL. The requested Lean version \`${this.leanVersion}\` ` +
          `does not match the fetched Lean version in the repository \`${replLeanVersion ?? "unknown"}\`. ` +
          "Please open an issue on GitHub if you think this is a bug.",
      );
    }
  }

  private async setupVersionSpecificReplDir(): Promise<void> {
    const dir = join(this.cacheDir, ...this.repoName, `repl_${this.tagName(this.leanVersion)}`);
    this._cacheReplDir = dir;
    const cleanCommit = await new GitRepo(this.cacheCleanReplDir).headCommit();
    const lastSyncedFile = join(dir, ".last_synced_commit");

    await withFileLock(
      `${dir}.lock`,
      async () => {
        let lastSynced: string | null = null;
        if (existsSync(dir) && existsSync(lastSyncedFile)) {
          try {
            lastSynced = (await fs.readFile(lastSyncedFile, "utf8")).trim();
          } catch (err) {
            logger.warn("Could not read last synced commit file: %s", String(err));
          }
        }
        if (existsSync(dir) && lastSynced === cleanCommit) return;

        if (existsSync(dir)) {
          try {
            await fs.rm(dir, { recursive: true, force: true });
          } catch (err) {
            logger.error("Failed to remove old REPL cache directory: %s", String(err));
          }
        }
        try {
          await fs.mkdir(dir, { recursive: true });
          await fs.cp(this.cacheCleanReplDir, dir, { recursive: true, force: true });
          await fs.writeFile(lastSyncedFile, cleanCommit, "utf8");
          logger.info("Updated version-specific REPL cache to commit %s", cleanCommit);
        } catch (err) {
          logger.error("Failed to update REPL cache: %s", String(err));
          throw err;
        }
      },
      LOCK_TIMEOUT_MS,
    );
  }

  private async buildReplNow(): Promise<void> {
    await checkLake(this.lakePath, this.verbose);
    let res;
    try {
      res = await exec(this.lakePath, ["build"], { cwd: this.cacheReplDir, inherit: this.verbose });
    } catch (err) {
      if (err instanceof ExecutableNotFoundError) {
        throw new Error(
          `Lean 4 build system executable not found at \`${this.lakePath}\`. You can try to run ` +
            "`lean-interact install-lean` or follow: https://leanprover-community.github.io/get_started.html",
          { cause: err },
        );
      }
      throw err;
    }
    if (res.code !== 0) {
      const sep = "-".repeat(50);
      throw new Error(
        `Failed to build the REPL at ${this.cacheReplDir}\n${sep}\nstdout:\n${res.stdout}\n${sep}\nstderr:\n${res.stderr}\n${sep}`,
      );
    }
  }

  private async availableLeanVersions(): Promise<Array<[string, string | null]>> {
    if (this.localReplPath) {
      const v = getProjectLeanVersion(this.localReplPath);
      return v ? [[v, null]] : [];
    }
    const prefix = `${this.replRev}_lean-toolchain-`;
    const tags = (await new GitRepo(this.cacheCleanReplDir).tags()).filter((t) => t.startsWith(prefix));
    if (tags.length === 0) {
      const v = this._cacheReplDir ? getProjectLeanVersion(this._cacheReplDir) : null;
      return v ? [[v, null]] : [];
    }
    const versions: Array<[string, string | null]> = tags.map((t) => [t.split("_lean-toolchain-").at(-1)!, t]);
    return versions.sort((a, b) => compareLeanVersions(a[0], b[0]));
  }

  /** Lean versions available for the selected REPL, sorted ascending. */
  async getAvailableLeanVersions(): Promise<string[]> {
    return (await this.availableLeanVersions()).map((v) => v[0]);
  }

  /** Directory of the (built) REPL. */
  get cacheReplDir(): string {
    if (this._cacheReplDir === null) throw new Error("The Lean REPL has not been set up.");
    return this._cacheReplDir;
  }

  /** Directory where commands are executed: the project directory, or the REPL directory. */
  get workingDir(): string {
    if (this.project) return this.project.getDirectory();
    return this.cacheReplDir;
  }

  /** Path of the REPL executable. */
  get replBinaryPath(): string {
    return join(this.cacheReplDir, ".lake", "build", "bin", "repl");
  }

  /** Whether the Lean environment has been set up. */
  isSetup(): boolean {
    return this._cacheReplDir !== null;
  }
}
