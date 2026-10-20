package leaninteract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const configLockTimeout = 300 * time.Second

// ConfigOptions configure [NewConfig]. The zero value selects the latest Lean
// version supported by the default REPL, with all optimizations enabled.
type ConfigOptions struct {
	// LeanVersion to use, e.g. "v4.19.0". Must be empty when Project is set
	// (the version is then read from the project). Empty selects the latest
	// version available for the REPL revision.
	LeanVersion string
	// Project the REPL sessions run in. Nil means Lean and its standard
	// library only.
	Project Project
	// REPLRev is the REPL revision. The config first tries the tag
	// "<REPLRev>_lean-toolchain-<LeanVersion>" and falls back to REPLRev.
	// Defaults to DefaultREPLVersion. Ignored with LocalREPLPath.
	REPLRev string
	// REPLGit is the REPL repository. Defaults to DefaultREPLGitURL. Ignored
	// with LocalREPLPath.
	REPLGit string
	// ForcePullREPL always fetches the REPL repository before checkout.
	ForcePullREPL bool
	// CacheDir is where REPL copies are cached. Defaults to DefaultCacheDir().
	CacheDir string
	// LocalREPLPath uses an existing local REPL checkout instead of git.
	LocalREPLPath string
	// SkipBuildREPL skips `lake build` of the REPL (Python build_repl=False).
	SkipBuildREPL bool
	// LakePath is the lake executable. Defaults to "lake".
	LakePath string
	// MemoryHardLimitMB caps the REPL address space (Linux only). 0 means no
	// limit.
	MemoryHardLimitMB int
	// DisableIncrementalOptimization turns off `incrementality: true` on
	// commands (enabled by default, Lean >= v4.8.0-rc1).
	DisableIncrementalOptimization bool
	// DisableParallelElaboration turns off the `Elab.async` option on
	// commands (enabled by default, Lean >= v4.19.0).
	DisableParallelElaboration bool
	// Verbose prints setup and build output.
	Verbose bool
}

// Config is a prepared Lean REPL configuration shared by servers. Create one
// with [NewConfig] before starting concurrent servers.
type Config struct {
	// LeanVersion is the resolved Lean version.
	LeanVersion string
	// Project is the configured project, if any.
	Project Project

	REPLRev                       string
	REPLGit                       string
	ForcePullREPL                 bool
	CacheDir                      string
	LocalREPLPath                 string
	BuildREPL                     bool
	LakePath                      string
	MemoryHardLimitMB             int
	EnableIncrementalOptimization bool
	EnableParallelElaboration     bool
	Verbose                       bool

	repoName     string
	cleanREPLDir string
	cacheREPLDir string
}

// NewConfig prepares the REPL: it clones/checks out (or uses the local copy
// of) the REPL matching the Lean version, and builds it with lake.
func NewConfig(ctx context.Context, opts ConfigOptions) (*Config, error) {
	if opts.Project != nil && opts.LeanVersion != "" {
		return nil, errors.New("leaninteract: LeanVersion should only be set when Project is nil; the Lean version is inferred from the project")
	}
	c := &Config{
		Project:                       opts.Project,
		REPLRev:                       opts.REPLRev,
		REPLGit:                       opts.REPLGit,
		ForcePullREPL:                 opts.ForcePullREPL,
		CacheDir:                      opts.CacheDir,
		LocalREPLPath:                 opts.LocalREPLPath,
		BuildREPL:                     !opts.SkipBuildREPL,
		LakePath:                      opts.LakePath,
		MemoryHardLimitMB:             opts.MemoryHardLimitMB,
		EnableIncrementalOptimization: !opts.DisableIncrementalOptimization,
		EnableParallelElaboration:     !opts.DisableParallelElaboration,
		Verbose:                       opts.Verbose,
	}
	if c.REPLRev == "" {
		c.REPLRev = DefaultREPLVersion
	}
	if c.REPLGit == "" {
		c.REPLGit = DefaultREPLGitURL
	}
	if c.CacheDir == "" {
		c.CacheDir = DefaultCacheDir()
	}
	if c.LakePath == "" {
		c.LakePath = "lake"
	}
	if opts.LeanVersion != "" {
		v, err := ParseLeanVersion(opts.LeanVersion)
		if err != nil {
			return nil, fmt.Errorf("leaninteract: unable to parse Lean version format: `%s`", opts.LeanVersion)
		}
		c.LeanVersion = v
	}
	if c.Project != nil {
		if c.Project.Directory() == "" {
			return nil, errors.New("leaninteract: project directory cannot be empty")
		}
		v, err := c.Project.LeanVersion()
		if err != nil {
			return nil, err
		}
		c.LeanVersion = v
	}

	if c.LocalREPLPath != "" {
		if err := c.prepareLocalREPL(); err != nil {
			return nil, err
		}
	} else if err := c.prepareGitREPL(ctx); err != nil {
		return nil, err
	}
	if c.BuildREPL {
		if err := c.buildREPL(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Config) tagName(leanVersion string) string {
	return c.REPLRev + "_lean-toolchain-" + leanVersion
}

func (c *Config) prepareLocalREPL() error {
	if _, err := os.Stat(c.LocalREPLPath); err != nil {
		return fmt.Errorf("leaninteract: local REPL path '%s' does not exist", c.LocalREPLPath)
	}
	local := ProjectLeanVersion(c.LocalREPLPath)
	if local == "" {
		Logger.Warn("could not determine Lean version from local REPL", "path", c.LocalREPLPath)
	} else if c.LeanVersion != "" && c.LeanVersion != local {
		Logger.Warn("requested Lean version does not match version in local REPL", "requested", c.LeanVersion, "local", local)
	}
	if c.LeanVersion == "" {
		c.LeanVersion = local
	}
	c.cacheREPLDir = c.LocalREPLPath
	if c.Verbose {
		Logger.Info("using local REPL", "path", c.LocalREPLPath)
	}
	return nil
}

func repoNameFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) >= 2 {
		return filepath.Join(parts[len(parts)-2], strings.ReplaceAll(parts[len(parts)-1], ".git", ""))
	}
	return strings.ReplaceAll(url, ".git", "")
}

func (c *Config) prepareGitREPL(ctx context.Context) error {
	c.repoName = repoNameFromURL(c.REPLGit)
	c.cleanREPLDir = filepath.Join(c.CacheDir, c.repoName, "repl_clean_copy")

	unlock, err := acquireFileLock(ctx, c.cleanREPLDir+".lock", configLockTimeout)
	if err != nil {
		return err
	}
	defer unlock()

	if err := c.setupCleanREPLRepo(ctx); err != nil {
		return err
	}
	g := gitRepo{dir: c.cleanREPLDir}
	if c.ForcePullREPL {
		c.forceUpdateREPL(ctx, g)
	}
	ok := c.checkoutREPLRevision(ctx, g)
	if !ok && !c.ForcePullREPL {
		if err := c.retryCheckoutAfterPull(ctx, g); err != nil {
			return err
		}
	}
	if err := c.validateAndSetLeanVersion(ctx, g); err != nil {
		return err
	}
	return c.setupVersionSpecificREPLDir(ctx, g)
}

func (c *Config) setupCleanREPLRepo(ctx context.Context) error {
	if _, err := os.Stat(c.cleanREPLDir); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.cleanREPLDir), 0o755); err != nil {
		return err
	}
	if err := gitClone(ctx, c.REPLGit, c.cleanREPLDir); err != nil {
		os.RemoveAll(c.cleanREPLDir)
		Logger.Error("failed to clone REPL repository", "url", c.REPLGit, "err", err)
		return err
	}
	Logger.Debug("cloned REPL repository", "url", c.REPLGit)
	return nil
}

func (c *Config) forceUpdateREPL(ctx context.Context, g gitRepo) {
	if !g.safeFetch(ctx) {
		Logger.Warn("failed to fetch during force update")
		return
	}
	if c.LeanVersion != "" && g.safeCheckout(ctx, c.tagName(c.LeanVersion)) {
		// Tags do not move, no reset needed.
		return
	}
	target := ""
	current := g.currentBranch(ctx)
	if current != "" && g.remoteRefExists(ctx, "origin/"+current) {
		target = current
	} else if c.LeanVersion == "" {
		target = current
	}
	if target != "" && g.remoteRefExists(ctx, "origin/"+target) {
		if !g.safeResetHard(ctx, "origin/"+target) {
			Logger.Warn("failed to reset REPL to remote branch", "branch", target)
		}
	} else {
		Logger.Debug("force update: fetched all refs, but no matching remote branch for reset")
	}
}

func (c *Config) checkoutREPLRevision(ctx context.Context, g gitRepo) bool {
	if c.LeanVersion != "" {
		return g.safeCheckout(ctx, c.tagName(c.LeanVersion))
	}
	return g.safeCheckout(ctx, c.REPLRev)
}

func (c *Config) retryCheckoutAfterPull(ctx context.Context, g gitRepo) error {
	if !g.safePull(ctx) {
		Logger.Warn("failed to pull REPL repository, continuing with current state")
	}
	if c.LeanVersion != "" && g.safeCheckout(ctx, c.tagName(c.LeanVersion)) {
		return nil
	}
	if !g.safeCheckout(ctx, c.REPLRev) {
		return fmt.Errorf("leaninteract: Lean REPL version `%s` is not available", c.REPLRev)
	}
	return nil
}

func (c *Config) validateAndSetLeanVersion(ctx context.Context, g gitRepo) error {
	if c.LeanVersion == "" {
		c.cacheREPLDir = c.cleanREPLDir
		versions, err := c.availableLeanVersions(ctx)
		if err != nil {
			return err
		}
		if len(versions) > 0 {
			c.LeanVersion = versions[len(versions)-1]
			g.safeCheckout(ctx, c.tagName(c.LeanVersion))
		}
	}
	replVersion := ProjectLeanVersion(c.cleanREPLDir)
	if c.LeanVersion == "" {
		c.LeanVersion = replVersion
	}
	if replVersion == "" || c.LeanVersion != replVersion {
		if replVersion == "" {
			replVersion = "unknown"
		}
		return fmt.Errorf("leaninteract: an error occurred while preparing the Lean REPL. The requested Lean version `%s` does not match the fetched Lean version in the repository `%s`", c.LeanVersion, replVersion)
	}
	return nil
}

func (c *Config) setupVersionSpecificREPLDir(ctx context.Context, g gitRepo) error {
	c.cacheREPLDir = filepath.Join(c.CacheDir, c.repoName, "repl_"+c.tagName(c.LeanVersion))
	commit, err := g.headCommit(ctx)
	if err != nil {
		return err
	}
	unlock, err := acquireFileLock(ctx, c.cacheREPLDir+".lock", configLockTimeout)
	if err != nil {
		return err
	}
	defer unlock()

	syncedFile := filepath.Join(c.cacheREPLDir, ".last_synced_commit")
	last := ""
	if b, err := os.ReadFile(syncedFile); err == nil {
		last = strings.TrimSpace(string(b))
	}
	if last == commit {
		return nil
	}
	if err := os.RemoveAll(c.cacheREPLDir); err != nil {
		Logger.Error("failed to remove old REPL cache directory", "err", err)
	}
	if err := copyDir(c.cleanREPLDir, c.cacheREPLDir); err != nil {
		return fmt.Errorf("leaninteract: failed to update REPL cache: %w", err)
	}
	if err := os.WriteFile(syncedFile, []byte(commit), 0o644); err != nil {
		return err
	}
	Logger.Info("updated version-specific REPL cache", "commit", commit)
	return nil
}

func (c *Config) buildREPL(ctx context.Context) error {
	if err := CheckLake(ctx, c.LakePath, c.Verbose); err != nil {
		return err
	}
	res, err := runCmd(ctx, c.cacheREPLDir, c.Verbose, c.LakePath, "build")
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("leaninteract: Lean 4 build system executable not found at `%s`. %s", c.LakePath, leanInstallHint)
		}
		sep := strings.Repeat("-", 50)
		return fmt.Errorf("leaninteract: failed to build the REPL at %s: %w\n%s\nstdout:\n%s\n%s\nstderr:\n%s\n%s",
			c.cacheREPLDir, err, sep, res.stdout, sep, res.stderr, sep)
	}
	return nil
}

// availableLeanVersions lists the Lean versions with a REPL tag, sorted
// ascending.
func (c *Config) availableLeanVersions(ctx context.Context) ([]string, error) {
	if c.LocalREPLPath != "" {
		if v := ProjectLeanVersion(c.LocalREPLPath); v != "" {
			return []string{v}, nil
		}
		return nil, nil
	}
	prefix := c.REPLRev + "_lean-toolchain-"
	tags, err := gitRepo{dir: c.cleanREPLDir}.tags(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		if v := ProjectLeanVersion(c.cacheREPLDir); v != "" {
			return []string{v}, nil
		}
		return nil, nil
	}
	versions := make([]string, 0, len(tags))
	for _, t := range tags {
		_, v, _ := strings.Cut(t, "_lean-toolchain-")
		versions = append(versions, v)
	}
	SortLeanVersions(versions)
	return versions, nil
}

// AvailableLeanVersions returns the Lean versions available for the selected
// REPL, sorted ascending.
func (c *Config) AvailableLeanVersions(ctx context.Context) ([]string, error) {
	return c.availableLeanVersions(ctx)
}

// CacheREPLDir is the directory of the REPL checkout used by servers.
func (c *Config) CacheREPLDir() string { return c.cacheREPLDir }

// WorkingDir is where REPL commands execute: the project directory, or the
// REPL directory when there is no project.
func (c *Config) WorkingDir() string {
	if c.Project != nil {
		return c.Project.Directory()
	}
	return c.cacheREPLDir
}

// REPLBinary is the path of the built REPL executable.
func (c *Config) REPLBinary() string {
	name := "repl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(c.cacheREPLDir, ".lake", "build", "bin", name)
}

// IsSetup reports whether the REPL directory has been prepared.
func (c *Config) IsSetup() bool { return c != nil && c.cacheREPLDir != "" }

// copyDir copies the tree at src into dst, preserving modes and symlinks.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
