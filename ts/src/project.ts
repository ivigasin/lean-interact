/**
 * Lean project management: existing local directories, git repositories, and temporary projects
 * built from a custom lakefile or a list of dependencies.
 *
 * Port of `lean_interact.project`. Python dataclasses run their setup in `__post_init__`; here
 * each class has an async `create()` factory doing the same work.
 *
 * @module
 */

import { createHash } from "node:crypto";
import { existsSync, promises as fs } from "node:fs";
import { join, resolve } from "node:path";

import { exec, execChecked } from "./internal/exec.js";
import { withFileLock } from "./internal/filelock.js";
import { gitClone, GitRepo } from "./internal/git.js";
import { logger } from "./internal/log.js";
import { defaultCacheDir, repoPathParts } from "./internal/paths.js";
import { checkLake, getProjectLeanVersion } from "./utils.js";

export interface BaseProjectOptions {
  /** Path to the lake executable. Default `"lake"` (from `PATH`). */
  lakePath?: string;
  /** Build the project during `create()`. Default `true`. */
  autoBuild?: boolean;
}

export interface BuildOptions {
  /** Print build output. Default `true`. */
  verbose?: boolean;
  /** Run `lake update` before building. Default `false`. */
  update?: boolean;
  /** @internal Acquire the project file lock (false when the caller already holds it). */
  lock?: boolean;
}

/** Base class for Lean projects. */
export abstract class BaseProject {
  readonly lakePath: string;
  readonly autoBuild: boolean;
  protected _directory: string | null;

  protected constructor(directory: string | null, options: BaseProjectOptions) {
    this._directory = directory;
    this.lakePath = options.lakePath ?? "lake";
    this.autoBuild = options.autoBuild ?? true;
  }

  /** The configured directory (may be relative), or `null` if not yet determined. */
  get directory(): string | null {
    return this._directory;
  }

  /** Absolute directory of the project. */
  getDirectory(): string {
    if (this._directory === null) throw new Error("`directory` must be set");
    return resolve(this._directory);
  }

  /** Lean version used by this project (from its `lean-toolchain`). */
  getLeanVersion(): string {
    const version = getProjectLeanVersion(this.getDirectory());
    if (version === null) throw new Error("Unable to determine Lean version");
    return version;
  }

  /** Build the project with lake (`lake exe cache get` is attempted first, non-fatal). */
  async build(options: BuildOptions = {}): Promise<void> {
    const { verbose = true, update = false, lock = true } = options;
    const directory = this.getDirectory();
    await checkLake(this.lakePath, verbose);

    const doBuild = async () => {
      try {
        if (update) await execChecked(this.lakePath, ["update"], { cwd: directory, inherit: verbose });
        const cache = await exec(this.lakePath, ["exe", "cache", "get"], { cwd: directory, inherit: verbose });
        if (cache.code !== 0 && verbose) {
          logger.info("Getting 'error: unknown executable cache' is expected if the project doesn't depend on Mathlib");
        }
        await execChecked(this.lakePath, ["build"], { cwd: directory, inherit: verbose });
        logger.debug("Successfully built project at %s", directory);
      } catch (err) {
        logger.error("Failed to build the project: %s", String(err));
        throw err;
      }
    };

    if (lock) await withFileLock(`${directory}.lock`, doBuild);
    else await doBuild();
  }
}

/** An existing local Lean project directory. */
export class LocalProject extends BaseProject {
  private constructor(directory: string, options: BaseProjectOptions) {
    super(directory, options);
  }

  /**
   * @example
   * ```ts
   * const project = await LocalProject.create({ directory: "/path/to/my/lean/project" });
   * const config = await LeanREPLConfig.create({ project });
   * ```
   */
  static async create(options: BaseProjectOptions & { directory: string }): Promise<LocalProject> {
    if (!options.directory) throw new Error("`LocalProject` requires `directory` to be specified");
    const project = new LocalProject(options.directory, options);
    if (project.autoBuild) await project.build();
    return project;
  }
}

export interface GitProjectOptions extends BaseProjectOptions {
  /** Git URL of the repository to clone. */
  url: string;
  /** Clone destination. Default: `<cache>/git_projects/<owner>/<repo>/<rev or "latest">`. */
  directory?: string;
  /** Tag, branch or commit to check out. Default: the default branch. */
  rev?: string;
  /** Fetch and hard-reset to the remote, overwriting local changes. */
  forcePull?: boolean;
}

/** A Lean project in a git repository, cloned (or updated) on creation. */
export class GitProject extends BaseProject {
  readonly url: string;
  readonly rev: string | null;
  readonly forcePull: boolean;

  private constructor(options: GitProjectOptions) {
    super(options.directory ?? null, options);
    this.url = options.url;
    this.rev = options.rev ?? null;
    this.forcePull = options.forcePull ?? false;
    if (this._directory === null) {
      this._directory = join(defaultCacheDir(), "git_projects", ...repoPathParts(this.url), this.rev ?? "latest");
    }
  }

  /**
   * @example
   * ```ts
   * const project = await GitProject.create({ url: "https://github.com/user/lean-project", rev: "main" });
   * ```
   */
  static async create(options: GitProjectOptions): Promise<GitProject> {
    const project = new GitProject(options);
    const directory = project.getDirectory();
    await withFileLock(`${directory}.lock`, async () => {
      try {
        if (existsSync(directory)) await project.updateExistingRepo();
        else await project.cloneNewRepo();
        if (project.autoBuild) await project.build({ lock: false });
      } catch (err) {
        logger.error("Failed to instantiate git project at %s: %s", directory, String(err));
        throw err;
      }
    });
    return project;
  }

  private async updateExistingRepo(): Promise<void> {
    const git = new GitRepo(this.getDirectory());
    // Only make network calls when necessary, to avoid rate limiting.
    let networkCallsMade = false;

    if (this.forcePull) {
      await this.forceUpdateRepo(git);
      networkCallsMade = true;
    }

    if (this.rev) {
      if (!(await git.safeCheckout(this.rev))) {
        logger.debug("Revision '%s' not found locally, fetching from remote", this.rev);
        if (!(await git.safeFetch())) throw new Error(`Could not fetch from remote to get revision '${this.rev}'`);
        networkCallsMade = true;
        if (!(await git.safeCheckout(this.rev))) throw new Error(`Could not checkout revision '${this.rev}' after fetching`);
      }
    } else if (!networkCallsMade) {
      if (await git.safePull()) {
        networkCallsMade = true;
        logger.debug("Pulled latest changes for default branch");
      } else {
        logger.warn("Failed to pull from remote, continuing with current state");
      }
    }

    if (networkCallsMade || this.forcePull) {
      if (!(await git.updateSubmodules())) logger.warn("Failed to update submodules");
    } else {
      logger.debug("Skipping submodule update to minimize network calls");
    }
  }

  private async forceUpdateRepo(git: GitRepo): Promise<void> {
    if (!(await git.safeFetch())) throw new Error("Failed to fetch from remote during force update");
    logger.debug("Force update: successfully fetched latest changes from remote");

    let targetBranch: string | null = null;
    if (this.rev && (await git.branchExistsLocally(this.rev))) targetBranch = this.rev;
    else if (!this.rev) targetBranch = await git.getCurrentBranchName();

    if (targetBranch && (await git.remoteRefExists(`origin/${targetBranch}`))) {
      if (await git.safeResetHard(`origin/${targetBranch}`)) {
        logger.info("Force updated git project to match remote branch %s", targetBranch);
      } else {
        logger.warn("Failed to reset to remote branch %s", targetBranch);
      }
    } else {
      logger.info("Force pull: fetched all refs, but no matching remote branch for reset.");
    }
    if (!(await git.updateSubmodules())) logger.warn("Failed to update submodules after force update");
  }

  private async cloneNewRepo(): Promise<void> {
    const directory = this.getDirectory();
    try {
      await gitClone(this.url, directory);
      logger.debug("Successfully cloned repository from %s", this.url);
      const git = new GitRepo(directory);
      if (this.rev && !(await git.safeCheckout(this.rev))) {
        throw new Error(`Could not checkout revision '${this.rev}' after cloning`);
      }
      if (!(await git.updateSubmodules())) logger.warn("Failed to update submodules after cloning");
    } catch (err) {
      logger.error("Failed to clone repository from %s: %s", this.url, String(err));
      throw err;
    }
  }
}

export interface BaseTempProjectOptions extends BaseProjectOptions {
  /** Lean version of the project, e.g. `"v4.19.0"`. */
  leanVersion: string;
  /** Project directory. Default: `<cache>/tmp_projects/<leanVersion>/<content hash>`. */
  directory?: string;
  /** Print setup output. Default `true`. */
  verbose?: boolean;
}

/** Base class for temporary projects created with `lake init` and cached by content hash. */
export abstract class BaseTempProject extends BaseProject {
  readonly leanVersion: string;
  readonly verbose: boolean;

  protected constructor(options: BaseTempProjectOptions) {
    super(options.directory ?? null, options);
    this.leanVersion = options.leanVersion;
    this.verbose = options.verbose ?? true;
  }

  /** Unique hash of the project content (used for the cache directory). */
  protected abstract hashContent(): string;

  /** Write or modify the lakefile after `lake init`. */
  protected abstract modifyLakefile(): Promise<void>;

  /** Shared setup logic (Python `BaseTempProject.__post_init__`). */
  protected async setup(): Promise<void> {
    if (this._directory === null) {
      this._directory = join(defaultCacheDir(), "tmp_projects", this.leanVersion, this.hashContent());
    }
    const directory = this.getDirectory();
    await fs.mkdir(directory, { recursive: true });

    await withFileLock(`${directory}.lock`, async () => {
      if (existsSync(join(directory, "lake-manifest.json"))) return;

      // Clean the folder in case of a previous aborted build.
      await fs.rm(directory, { recursive: true, force: true });
      await fs.mkdir(directory, { recursive: true });

      const minor = Number(this.leanVersion.split(".")[1]);
      const template = this.leanVersion.startsWith("v4") && minor <= 7 ? "exe" : "exe.lean";
      try {
        await execChecked(this.lakePath, [`+${this.leanVersion}`, "init", "dummy", template], {
          cwd: directory,
          inherit: this.verbose,
        });
      } catch (err) {
        logger.error("Failed to initialize Lean project: %s", String(err));
        throw err;
      }

      await this.modifyLakefile();
      logger.info("Preparing Lean environment with dependencies (may take a while the first time)...");
      try {
        if (this.autoBuild) await this.build({ verbose: this.verbose, update: true, lock: false });
      } catch (err) {
        logger.error("Failed during Lean project setup: %s", String(err));
        await fs.rm(directory, { recursive: true, force: true });
        throw err;
      }
    });
  }
}

export interface TemporaryProjectOptions extends BaseTempProjectOptions {
  /** Lakefile content. */
  content: string;
  /** `"lean"` for `lakefile.lean` (default) or `"toml"` for `lakefile.toml`. */
  lakefileType?: "lean" | "toml";
}

/** A temporary project with a custom lakefile. */
export class TemporaryProject extends BaseTempProject {
  readonly content: string;
  readonly lakefileType: "lean" | "toml";

  private constructor(options: TemporaryProjectOptions) {
    super(options);
    this.content = options.content;
    this.lakefileType = options.lakefileType ?? "lean";
  }

  /**
   * @example
   * ```ts
   * const project = await TemporaryProject.create({
   *   leanVersion: "v4.19.0",
   *   content: `import Lake\nopen Lake DSL\n\npackage "my_temp_project"\n\nrequire mathlib from git\n  "https://github.com/leanprover-community/mathlib4.git" @ "v4.19.0"`,
   * });
   * ```
   */
  static async create(options: TemporaryProjectOptions): Promise<TemporaryProject> {
    const project = new TemporaryProject(options);
    await project.setup();
    return project;
  }

  protected hashContent(): string {
    return createHash("sha256").update(this.content).digest("hex");
  }

  protected async modifyLakefile(): Promise<void> {
    const filename = this.lakefileType === "lean" ? "lakefile.lean" : "lakefile.toml";
    await fs.writeFile(join(this.getDirectory(), filename), this.content, "utf8");
  }
}

/** A dependency for a `lakefile.lean`. */
export class LeanRequire {
  constructor(
    /** Package name. */
    readonly name: string,
    /** Git URL. */
    readonly git: string,
    /** Tag, branch or commit; `null` for the default branch. */
    readonly rev: string | null = null,
  ) {}

  /** Python `repr()` of the dataclass; used for the cache hash so it matches the Python port. */
  pyRepr(): string {
    return `LeanRequire(name=${pyStr(this.name)}, git=${pyStr(this.git)}, rev=${this.rev === null ? "None" : pyStr(this.rev)})`;
  }
}

function pyStr(s: string): string {
  const quote = s.includes("'") && !s.includes('"') ? '"' : "'";
  const body = s
    .replace(/\\/g, "\\\\")
    .replace(/\n/g, "\\n")
    .replace(/\r/g, "\\r")
    .replace(/\t/g, "\\t")
    .replace(new RegExp(quote, "g"), "\\" + quote);
  return quote + body + quote;
}

export type Requirement = "mathlib" | LeanRequire;

export interface TempRequireProjectOptions extends BaseTempProjectOptions {
  /** `"mathlib"` (a Mathlib matching `leanVersion`), a {@link LeanRequire}, or a list of these. */
  require: Requirement | Requirement[];
}

/** A temporary project with the given dependencies. */
export class TempRequireProject extends BaseTempProject {
  readonly require: Requirement | Requirement[];

  private constructor(options: TempRequireProjectOptions) {
    super(options);
    this.require = options.require;
  }

  /**
   * @example
   * ```ts
   * const project = await TempRequireProject.create({ leanVersion: "v4.19.0", require: "mathlib" });
   * ```
   */
  static async create(options: TempRequireProjectOptions): Promise<TempRequireProject> {
    const project = new TempRequireProject(options);
    await project.setup();
    return project;
  }

  /** Requirements as a sorted list of {@link LeanRequire}. */
  normalizeRequire(): LeanRequire[] {
    const list = Array.isArray(this.require) ? this.require : [this.require];
    const out = list.map((req) => {
      if (req === "mathlib") {
        return new LeanRequire("mathlib", "https://github.com/leanprover-community/mathlib4.git", this.leanVersion);
      }
      if (req instanceof LeanRequire) return req;
      throw new TypeError(`Invalid requirement type: ${typeof req}`);
    });
    return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  }

  protected hashContent(): string {
    const repr = `[${this.normalizeRequire()
      .map((r) => r.pyRepr())
      .join(", ")}]`;
    return createHash("sha256").update(repr).digest("hex");
  }

  protected async modifyLakefile(): Promise<void> {
    let text = "";
    for (const req of this.normalizeRequire()) {
      text += `\n\nrequire ${req.name} from git\n  "${req.git}"` + (req.rev ? ` @ "${req.rev}"` : "");
    }
    await fs.appendFile(join(this.getDirectory(), "lakefile.lean"), text, "utf8");
  }
}
