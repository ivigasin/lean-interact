package leaninteract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Project is a Lean project the REPL runs in.
type Project interface {
	// Directory is the absolute project directory.
	Directory() string
	// LeanVersion is the Lean version from the project's lean-toolchain.
	LeanVersion() (string, error)
	// Build builds the project with lake.
	Build(ctx context.Context, opts BuildOptions) error
}

// BuildOptions configure Project.Build.
type BuildOptions struct {
	// Verbose streams lake output to the terminal.
	Verbose bool
	// Update runs `lake update` before building.
	Update bool
	// noLock is set when the caller already holds the project lock.
	noLock bool
}

// baseProject implements Project for a directory.
type baseProject struct {
	dir      string
	lakePath string
}

func newBaseProject(dir, lakePath string) (baseProject, error) {
	if lakePath == "" {
		lakePath = "lake"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return baseProject{}, err
	}
	return baseProject{dir: abs, lakePath: lakePath}, nil
}

func (p *baseProject) Directory() string { return p.dir }

func (p *baseProject) LeanVersion() (string, error) {
	v := ProjectLeanVersion(p.dir)
	if v == "" {
		return "", errors.New("leaninteract: unable to determine Lean version")
	}
	return v, nil
}

// Build runs `lake update` (optional), `lake exe cache get` (allowed to fail,
// e.g. without Mathlib) and `lake build`, under a lock on "<dir>.lock".
func (p *baseProject) Build(ctx context.Context, opts BuildOptions) error {
	if err := CheckLake(ctx, p.lakePath, opts.Verbose); err != nil {
		return err
	}
	if !opts.noLock {
		unlock, err := acquireFileLock(ctx, p.dir+".lock", 0)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if opts.Update {
		if _, err := runCmd(ctx, p.dir, opts.Verbose, p.lakePath, "update"); err != nil {
			return fmt.Errorf("leaninteract: `lake update` failed in %s: %w", p.dir, err)
		}
	}
	if _, err := runCmd(ctx, p.dir, opts.Verbose, p.lakePath, "exe", "cache", "get"); err != nil && opts.Verbose {
		Logger.Info("Getting 'error: unknown executable cache' is expected if the project doesn't depend on Mathlib")
	}
	if res, err := runCmd(ctx, p.dir, opts.Verbose, p.lakePath, "build"); err != nil {
		return fmt.Errorf("leaninteract: failed to build the project at %s: %w\n%s%s", p.dir, err, res.stdout, res.stderr)
	}
	Logger.Debug("built project", "dir", p.dir)
	return nil
}

// LocalProjectOptions configure [NewLocalProject].
type LocalProjectOptions struct {
	// Directory of the existing Lean project. Required.
	Directory string
	// LakePath defaults to "lake".
	LakePath string
	// NoAutoBuild skips building the project on creation.
	NoAutoBuild bool
	// Verbose streams build output.
	Verbose bool
}

// LocalProject is an existing Lean project directory.
type LocalProject struct{ baseProject }

// NewLocalProject uses an existing local project, building it unless
// NoAutoBuild is set.
func NewLocalProject(ctx context.Context, opts LocalProjectOptions) (*LocalProject, error) {
	if opts.Directory == "" {
		return nil, errors.New("leaninteract: LocalProject requires Directory to be specified")
	}
	b, err := newBaseProject(opts.Directory, opts.LakePath)
	if err != nil {
		return nil, err
	}
	p := &LocalProject{b}
	if !opts.NoAutoBuild {
		if err := p.Build(ctx, BuildOptions{Verbose: opts.Verbose}); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// GitProjectOptions configure [NewGitProject].
type GitProjectOptions struct {
	// URL of the git repository. Required.
	URL string
	// Directory to clone into. Defaults to
	// <DefaultCacheDir>/git_projects/<owner>/<repo>/<rev or "latest">.
	Directory string
	// Rev is a tag, branch or commit to check out. Empty uses the default branch.
	Rev string
	// ForcePull fetches and hard-resets to the remote branch, discarding local
	// changes.
	ForcePull bool
	// LakePath defaults to "lake".
	LakePath string
	// NoAutoBuild skips building the project.
	NoAutoBuild bool
	// Verbose streams build output.
	Verbose bool
}

// GitProject is a Lean project cloned from a git repository.
type GitProject struct {
	baseProject
	URL       string
	Rev       string
	ForcePull bool
}

// NewGitProject clones (or updates) the repository and builds it.
func NewGitProject(ctx context.Context, opts GitProjectOptions) (*GitProject, error) {
	if opts.URL == "" {
		return nil, errors.New("leaninteract: GitProject requires URL")
	}
	dir := opts.Directory
	if dir == "" {
		rev := opts.Rev
		if rev == "" {
			rev = "latest"
		}
		dir = filepath.Join(DefaultCacheDir(), "git_projects", repoNameFromURL(opts.URL), rev)
	}
	b, err := newBaseProject(dir, opts.LakePath)
	if err != nil {
		return nil, err
	}
	p := &GitProject{baseProject: b, URL: opts.URL, Rev: opts.Rev, ForcePull: opts.ForcePull}

	unlock, err := acquireFileLock(ctx, p.dir+".lock", 0)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, statErr := os.Stat(p.dir); statErr == nil {
		err = p.updateExistingRepo(ctx)
	} else {
		err = p.cloneNewRepo(ctx)
	}
	if err == nil && !opts.NoAutoBuild {
		err = p.Build(ctx, BuildOptions{Verbose: opts.Verbose, noLock: true})
	}
	if err != nil {
		Logger.Error("failed to instantiate git project", "dir", p.dir, "err", err)
		return nil, err
	}
	return p, nil
}

func (p *GitProject) updateExistingRepo(ctx context.Context) error {
	g := gitRepo{dir: p.dir}
	network := false
	if p.ForcePull {
		if err := p.forceUpdateRepo(ctx, g); err != nil {
			return err
		}
		network = true
	}
	if p.Rev != "" {
		if !g.safeCheckout(ctx, p.Rev) {
			Logger.Debug("revision not found locally, fetching from remote", "rev", p.Rev)
			if !g.safeFetch(ctx) {
				return fmt.Errorf("leaninteract: could not fetch from remote to get revision '%s'", p.Rev)
			}
			network = true
			if !g.safeCheckout(ctx, p.Rev) {
				return fmt.Errorf("leaninteract: could not checkout revision '%s' after fetching", p.Rev)
			}
		}
	} else if !network {
		if g.safePull(ctx) {
			network = true
		} else {
			Logger.Warn("failed to pull from remote, continuing with current state")
		}
	}
	if network || p.ForcePull {
		if !g.updateSubmodules(ctx) {
			Logger.Warn("failed to update submodules")
		}
	}
	return nil
}

func (p *GitProject) forceUpdateRepo(ctx context.Context, g gitRepo) error {
	if !g.safeFetch(ctx) {
		return errors.New("leaninteract: failed to fetch from remote during force update")
	}
	target := ""
	if p.Rev != "" && g.branchExistsLocally(ctx, p.Rev) {
		target = p.Rev
	} else if p.Rev == "" {
		target = g.currentBranch(ctx)
	}
	if target != "" && g.remoteRefExists(ctx, "origin/"+target) {
		if g.safeResetHard(ctx, "origin/"+target) {
			Logger.Info("force updated git project to match remote branch", "branch", target)
		} else {
			Logger.Warn("failed to reset to remote branch", "branch", target)
		}
	} else {
		Logger.Info("force pull: fetched all refs, but no matching remote branch for reset")
	}
	if !g.updateSubmodules(ctx) {
		Logger.Warn("failed to update submodules after force update")
	}
	return nil
}

func (p *GitProject) cloneNewRepo(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(p.dir), 0o755); err != nil {
		return err
	}
	if err := gitClone(ctx, p.URL, p.dir); err != nil {
		return err
	}
	g := gitRepo{dir: p.dir}
	if p.Rev != "" && !g.safeCheckout(ctx, p.Rev) {
		return fmt.Errorf("leaninteract: could not checkout revision '%s' after cloning", p.Rev)
	}
	if !g.updateSubmodules(ctx) {
		Logger.Warn("failed to update submodules after cloning")
	}
	return nil
}

// TempProjectOptions are shared by temporary projects.
type TempProjectOptions struct {
	// LeanVersion of the project, e.g. "v4.19.0". Required.
	LeanVersion string
	// Directory for the project. Defaults to a content-addressed directory
	// <DefaultCacheDir>/tmp_projects/<LeanVersion>/<hash>, so identical
	// projects are reused.
	Directory string
	// LakePath defaults to "lake".
	LakePath string
	// NoAutoBuild skips `lake update` + build after creation.
	NoAutoBuild bool
	// Verbose streams lake output.
	Verbose bool
}

// TemporaryProject is a generated project with a custom lakefile.
type TemporaryProject struct {
	baseProject
	// Content of the lakefile.
	Content string
	// LakefileType is "lean" (lakefile.lean) or "toml" (lakefile.toml).
	LakefileType string
}

// NewTemporaryProject creates (or reuses) a project whose lakefile is content.
// lakefileType is "lean" (the default when empty) or "toml".
func NewTemporaryProject(ctx context.Context, opts TempProjectOptions, content, lakefileType string) (*TemporaryProject, error) {
	if lakefileType == "" {
		lakefileType = "lean"
	}
	if lakefileType != "lean" && lakefileType != "toml" {
		return nil, fmt.Errorf("leaninteract: lakefile type must be \"lean\" or \"toml\", got %q", lakefileType)
	}
	sum := sha256.Sum256([]byte(content))
	p := &TemporaryProject{Content: content, LakefileType: lakefileType}
	err := setupTempProject(ctx, opts, &p.baseProject, hex.EncodeToString(sum[:]), func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "lakefile."+lakefileType), []byte(content), 0o644)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// LeanRequire is a lakefile.lean git dependency.
type LeanRequire struct {
	Name string
	Git  string
	// Rev is a tag, branch or commit. Empty uses the default branch.
	Rev string
}

// Mathlib is a shortcut requirement resolved to the Mathlib release matching
// the project's Lean version.
var Mathlib = LeanRequire{Name: "mathlib"}

// String matches the Python dataclass repr; it feeds the project hash.
func (r LeanRequire) String() string {
	rev := "None"
	if r.Rev != "" {
		rev = "'" + r.Rev + "'"
	}
	return fmt.Sprintf("LeanRequire(name='%s', git='%s', rev=%s)", r.Name, r.Git, rev)
}

// TempRequireProject is a generated project with the given dependencies.
type TempRequireProject struct {
	baseProject
	Require []LeanRequire
}

// NewTempRequireProject creates (or reuses) a project depending on require.
// Use [Mathlib] for a Mathlib dependency matching opts.LeanVersion.
func NewTempRequireProject(ctx context.Context, opts TempProjectOptions, require ...LeanRequire) (*TempRequireProject, error) {
	reqs := normalizeRequire(require, opts.LeanVersion)
	strs := make([]string, len(reqs))
	for i, r := range reqs {
		strs[i] = r.String()
	}
	sum := sha256.Sum256([]byte("[" + strings.Join(strs, ", ") + "]"))
	p := &TempRequireProject{Require: reqs}
	err := setupTempProject(ctx, opts, &p.baseProject, hex.EncodeToString(sum[:]), func(dir string) error {
		f, err := os.OpenFile(filepath.Join(dir, "lakefile.lean"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		for _, r := range reqs {
			line := fmt.Sprintf("\n\nrequire %s from git\n  \"%s\"", r.Name, r.Git)
			if r.Rev != "" {
				line += fmt.Sprintf(" @ \"%s\"", r.Rev)
			}
			if _, err := f.WriteString(line); err != nil {
				f.Close()
				return err
			}
		}
		return f.Close()
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func normalizeRequire(require []LeanRequire, leanVersion string) []LeanRequire {
	out := make([]LeanRequire, 0, len(require))
	for _, r := range require {
		if r.Name == "mathlib" && r.Git == "" {
			r = LeanRequire{Name: "mathlib", Git: "https://github.com/leanprover-community/mathlib4.git", Rev: leanVersion}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// lakeInitTemplate returns the `lake init` template: "exe" for Lean <= v4.7,
// "exe.lean" afterwards.
func lakeInitTemplate(leanVersion string) string {
	if strings.HasPrefix(leanVersion, "v4") {
		if parts := strings.Split(leanVersion, "."); len(parts) > 1 {
			if minor, err := strconv.Atoi(parts[1]); err == nil && minor <= 7 {
				return "exe"
			}
		}
	}
	return "exe.lean"
}

func setupTempProject(ctx context.Context, opts TempProjectOptions, bp *baseProject, hash string, writeLakefile func(dir string) error) error {
	if opts.LeanVersion == "" {
		return errors.New("leaninteract: temporary projects require LeanVersion")
	}
	dir := opts.Directory
	if dir == "" {
		dir = filepath.Join(DefaultCacheDir(), "tmp_projects", opts.LeanVersion, hash)
	}
	b, err := newBaseProject(dir, opts.LakePath)
	if err != nil {
		return err
	}
	*bp = b
	if err := os.MkdirAll(bp.dir, 0o755); err != nil {
		return err
	}
	unlock, err := acquireFileLock(ctx, bp.dir+".lock", 0)
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := os.Stat(filepath.Join(bp.dir, "lake-manifest.json")); err == nil {
		return nil
	}
	// Clean up a previously aborted setup.
	os.RemoveAll(bp.dir)
	if err := os.MkdirAll(bp.dir, 0o755); err != nil {
		return err
	}
	if _, err := runCmd(ctx, bp.dir, opts.Verbose, bp.lakePath, "+"+opts.LeanVersion, "init", "dummy", lakeInitTemplate(opts.LeanVersion)); err != nil {
		Logger.Error("failed to initialize Lean project", "err", err)
		return fmt.Errorf("leaninteract: failed to initialize Lean project: %w", err)
	}
	if err := writeLakefile(bp.dir); err != nil {
		return err
	}
	Logger.Info("Preparing Lean environment with dependencies (may take a while the first time)...")
	if !opts.NoAutoBuild {
		if err := bp.Build(ctx, BuildOptions{Verbose: opts.Verbose, Update: true, noLock: true}); err != nil {
			Logger.Error("failed during Lean project setup", "err", err)
			os.RemoveAll(bp.dir)
			return err
		}
	}
	return nil
}
