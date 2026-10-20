package leaninteract

import (
	"context"
	"os/exec"
	"strings"
)

// gitRepo wraps the git CLI for a working copy. The "safe" methods log and
// return false instead of failing, mirroring the Python _GitUtilities class.
type gitRepo struct {
	dir string
}

func (g gitRepo) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &gitError{args: args, out: strings.TrimSpace(string(out)), err: err}
	}
	return string(out), nil
}

type gitError struct {
	args []string
	out  string
	err  error
}

func (e *gitError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + e.out
}

func (e *gitError) Unwrap() error { return e.err }

func gitClone(ctx context.Context, url, dir string) error {
	_, err := gitRepo{}.git(ctx, "clone", url, dir)
	return err
}

func (g gitRepo) safeCheckout(ctx context.Context, rev string) bool {
	if _, err := g.git(ctx, "checkout", rev); err != nil {
		Logger.Debug("failed to checkout revision", "rev", rev, "err", err)
		return false
	}
	Logger.Debug("checked out revision", "rev", rev)
	return true
}

func (g gitRepo) safeFetch(ctx context.Context) bool {
	if _, err := g.git(ctx, "fetch", "origin", "--tags"); err != nil {
		Logger.Warn("failed to fetch from remote 'origin'", "err", err)
		return false
	}
	return true
}

// safePull fetches all refs and tags, then merges the upstream branch when a
// branch is checked out. On a detached HEAD (the usual state after checking
// out a REPL tag) only the fetch happens, which is what makes newly published
// tags available.
func (g gitRepo) safePull(ctx context.Context) bool {
	if !g.safeFetch(ctx) {
		return false
	}
	if g.currentBranch(ctx) == "" {
		return true
	}
	if _, err := g.git(ctx, "pull", "origin"); err != nil {
		Logger.Warn("failed to pull from remote 'origin'", "err", err)
		return false
	}
	return true
}

func (g gitRepo) safeResetHard(ctx context.Context, target string) bool {
	if _, err := g.git(ctx, "reset", "--hard", target); err != nil {
		Logger.Warn("failed to reset", "target", target, "err", err)
		return false
	}
	return true
}

func (g gitRepo) updateSubmodules(ctx context.Context) bool {
	if _, err := g.git(ctx, "submodule", "update", "--init", "--recursive"); err != nil {
		Logger.Warn("failed to update submodules", "err", err)
		return false
	}
	return true
}

// currentBranch returns the checked out branch, or "" on a detached HEAD.
func (g gitRepo) currentBranch(ctx context.Context) string {
	out, err := g.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (g gitRepo) refExists(ctx context.Context, ref string) bool {
	_, err := g.git(ctx, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func (g gitRepo) branchExistsLocally(ctx context.Context, branch string) bool {
	return g.refExists(ctx, "refs/heads/"+branch)
}

// remoteRefExists checks a remote-tracking ref such as "origin/main".
func (g gitRepo) remoteRefExists(ctx context.Context, ref string) bool {
	return g.refExists(ctx, "refs/remotes/"+ref)
}

func (g gitRepo) headCommit(ctx context.Context) (string, error) {
	out, err := g.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func (g gitRepo) tags(ctx context.Context, prefix string) ([]string, error) {
	out, err := g.git(ctx, "tag", "--list", prefix+"*")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tags = append(tags, l)
		}
	}
	return tags, nil
}
