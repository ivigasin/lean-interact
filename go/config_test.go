package leaninteract

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newREPLRepo creates a git repository mimicking the REPL tag layout.
func newREPLRepo(t *testing.T, versions ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "owner", "repl")
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q", "-b", "master")
	for _, v := range versions {
		os.WriteFile(filepath.Join(dir, "lean-toolchain"), []byte("leanprover/lean4:"+v+"\n"), 0o644)
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", v)
		git(t, dir, "tag", DefaultREPLVersion+"_lean-toolchain-"+v)
	}
	// The plain revision exists too, as in the real REPL repository.
	git(t, dir, "tag", DefaultREPLVersion)
	return dir
}

func TestConfigGitREPL(t *testing.T) {
	repo := newREPLRepo(t, "v4.9.0-rc1", "v4.9.0", "v4.10.0", "v4.8.0")
	cache := t.TempDir()
	cfg, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeanVersion != "v4.10.0" {
		t.Fatalf("latest version: %q", cfg.LeanVersion)
	}
	wantDir := filepath.Join(cache, "owner", "repl", "repl_"+DefaultREPLVersion+"_lean-toolchain-v4.10.0")
	if cfg.CacheREPLDir() != wantDir || cfg.WorkingDir() != wantDir {
		t.Fatalf("cache dir %q", cfg.CacheREPLDir())
	}
	if ProjectLeanVersion(wantDir) != "v4.10.0" {
		t.Fatal("version-specific copy has the wrong checkout")
	}
	synced, _ := os.ReadFile(filepath.Join(wantDir, ".last_synced_commit"))
	if strings.TrimSpace(string(synced)) != git(t, repo, "rev-list", "-n1", DefaultREPLVersion+"_lean-toolchain-v4.10.0") {
		t.Fatalf("last synced commit %q", synced)
	}
	versions, err := cfg.AvailableLeanVersions(bg)
	if err != nil || !slices.Equal(versions, []string{"v4.8.0", "v4.9.0-rc1", "v4.9.0", "v4.10.0"}) {
		t.Fatalf("versions %v %v", versions, err)
	}

	// A specific version reuses the clean clone.
	cfg2, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true, LeanVersion: "leanprover/lean4:v4.9.0-rc1"})
	if err != nil || cfg2.LeanVersion != "v4.9.0-rc1" || ProjectLeanVersion(cfg2.CacheREPLDir()) != "v4.9.0-rc1" {
		t.Fatalf("specific version: %+v %v", cfg2, err)
	}

	// A tag published after the first clone is found by the retry-after-pull path.
	os.WriteFile(filepath.Join(repo, "lean-toolchain"), []byte("leanprover/lean4:v4.11.0\n"), 0o644)
	git(t, repo, "commit", "-q", "-am", "v4.11.0")
	git(t, repo, "tag", DefaultREPLVersion+"_lean-toolchain-v4.11.0")
	cfg3, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true, LeanVersion: "v4.11.0"})
	if err != nil || ProjectLeanVersion(cfg3.CacheREPLDir()) != "v4.11.0" {
		t.Fatalf("new tag: %v", err)
	}
	// And with ForcePullREPL.
	if _, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true, LeanVersion: "v4.11.0", ForcePullREPL: true}); err != nil {
		t.Fatal(err)
	}

	// Unknown versions fail with a clear error.
	if _, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true, LeanVersion: "v4.99.0"}); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v", err)
	}
	if _, err := NewConfig(bg, ConfigOptions{REPLGit: repo, CacheDir: cache, SkipBuildREPL: true, REPLRev: "nope"}); err == nil {
		t.Fatal("invalid rev accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := NewConfig(bg, ConfigOptions{LeanVersion: "v3.1"}); err == nil {
		t.Fatal("lean 3 accepted")
	}
	if _, err := NewConfig(bg, ConfigOptions{LocalREPLPath: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing local REPL accepted")
	}
	lake, replDir := newFakeEnv(t)
	proj, err := NewLocalProject(bg, LocalProjectOptions{Directory: replDir, LakePath: lake})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfig(bg, ConfigOptions{Project: proj, LeanVersion: "v4.19.0", LocalREPLPath: replDir}); err == nil {
		t.Fatal("project + lean version accepted")
	}
	cfg, err := NewConfig(bg, ConfigOptions{Project: proj, LocalREPLPath: replDir, LakePath: lake})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkingDir() != proj.Directory() || cfg.LeanVersion != "v4.19.0" {
		t.Fatalf("project config %+v", cfg)
	}
	// Building the REPL runs `lake build` in the REPL dir.
	log := filepath.Join(t.TempDir(), "lake.log")
	t.Setenv("FAKE_LAKE_LOG", log)
	if _, err := NewConfig(bg, ConfigOptions{LocalREPLPath: replDir, LakePath: lake}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "build") {
		t.Fatalf("lake calls: %s", b)
	}
	if _, err := NewConfig(bg, ConfigOptions{LocalREPLPath: replDir, LakePath: filepath.Join(t.TempDir(), "nolake")}); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v", err)
	}
}

func TestTempProjects(t *testing.T) {
	lake, _ := newFakeEnv(t)
	t.Setenv("LEAN_INTERACT_CACHE_DIR", t.TempDir())
	log := filepath.Join(t.TempDir(), "lake.log")
	t.Setenv("FAKE_LAKE_LOG", log)

	opts := TempProjectOptions{LeanVersion: "v4.19.0", LakePath: lake}
	p, err := NewTempRequireProject(bg, opts, Mathlib, LeanRequire{Name: "aesop", Git: "https://github.com/leanprover-community/aesop", Rev: "v4.19.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Directory(), filepath.Join(DefaultCacheDir(), "tmp_projects", "v4.19.0")) {
		t.Fatalf("dir %s", p.Directory())
	}
	lakefile, _ := os.ReadFile(filepath.Join(p.Directory(), "lakefile.lean"))
	want := "\n\nrequire aesop from git\n  \"https://github.com/leanprover-community/aesop\" @ \"v4.19.0\"" +
		"\n\nrequire mathlib from git\n  \"https://github.com/leanprover-community/mathlib4.git\" @ \"v4.19.0\""
	if !strings.HasSuffix(string(lakefile), want) {
		t.Fatalf("lakefile:\n%s", lakefile)
	}
	if v, err := p.LeanVersion(); err != nil || v != "v4.19.0" {
		t.Fatalf("version %q %v", v, err)
	}
	calls, _ := os.ReadFile(log)
	for _, c := range []string{"+v4.19.0 init dummy exe.lean", "update", "exe cache get", "build"} {
		if !strings.Contains(string(calls), c) {
			t.Fatalf("missing lake call %q in:\n%s", c, calls)
		}
	}
	// Same requirements, same directory, no re-init.
	os.Remove(log)
	p2, err := NewTempRequireProject(bg, opts, LeanRequire{Name: "aesop", Git: "https://github.com/leanprover-community/aesop", Rev: "v4.19.0"}, Mathlib)
	if err != nil || p2.Directory() != p.Directory() {
		t.Fatalf("not reused: %v", err)
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "init") {
		t.Fatal("project re-initialized")
	}

	old := TempProjectOptions{LeanVersion: "v4.7.0", LakePath: lake}
	tp, err := NewTemporaryProject(bg, old, "[package]\nname = \"x\"\n", "toml")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tp.Directory(), "lakefile.toml")); !strings.Contains(string(b), "name = \"x\"") {
		t.Fatal("toml lakefile not written")
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "+v4.7.0 init dummy exe\n") {
		t.Fatalf("old lean init: %s", calls)
	}
}

func TestGitProject(t *testing.T) {
	repo := newREPLRepo(t, "v4.18.0", "v4.19.0")
	git(t, repo, "checkout", "-q", "-b", "feature")
	lake, _ := newFakeEnv(t)
	t.Setenv("LEAN_INTERACT_CACHE_DIR", t.TempDir())

	p, err := NewGitProject(bg, GitProjectOptions{URL: repo, Rev: DefaultREPLVersion + "_lean-toolchain-v4.18.0", LakePath: lake})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Directory(), filepath.Join(DefaultCacheDir(), "git_projects", "owner", "repl")) {
		t.Fatalf("dir %s", p.Directory())
	}
	if v, _ := p.LeanVersion(); v != "v4.18.0" {
		t.Fatalf("version %q", v)
	}
	// Default branch, then an update with force pull.
	p2, err := NewGitProject(bg, GitProjectOptions{URL: repo, LakePath: lake, NoAutoBuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := p2.LeanVersion(); v != "v4.19.0" {
		t.Fatalf("version %q", v)
	}
	os.WriteFile(filepath.Join(repo, "lean-toolchain"), []byte("leanprover/lean4:v4.20.0\n"), 0o644)
	git(t, repo, "commit", "-q", "-am", "bump")
	p3, err := NewGitProject(bg, GitProjectOptions{URL: repo, LakePath: lake, NoAutoBuild: true, ForcePull: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := p3.LeanVersion(); v != "v4.20.0" {
		t.Fatalf("force pull version %q", v)
	}
	if _, err := NewGitProject(bg, GitProjectOptions{URL: repo, LakePath: lake, NoAutoBuild: true, Rev: "does-not-exist"}); err == nil {
		t.Fatal("bad rev accepted")
	}
}
