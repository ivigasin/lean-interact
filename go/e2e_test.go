package leaninteract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// End-to-end tests against a real Lean REPL. They need `lake` on PATH and
// network access for the first REPL clone/build, so they only run when
// LEAN_INTERACT_E2E=1. Expectations mirror the upstream Python test suite.

var (
	e2eOnce sync.Once
	e2eCfg  *Config
	e2eErr  error
)

func e2eConfig(t *testing.T) *Config {
	t.Helper()
	if os.Getenv("LEAN_INTERACT_E2E") != "1" {
		t.Skip("set LEAN_INTERACT_E2E=1 to run against a real Lean REPL")
	}
	e2eOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		e2eCfg, e2eErr = NewConfig(ctx, ConfigOptions{LeanVersion: os.Getenv("LEAN_INTERACT_E2E_LEAN_VERSION")})
	})
	if e2eErr != nil {
		t.Fatalf("NewConfig: %v", e2eErr)
	}
	return e2eCfg
}

func e2eCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestE2EConfig(t *testing.T) {
	cfg := e2eConfig(t)
	if !strings.HasPrefix(cfg.LeanVersion, "v4") {
		t.Fatalf("unexpected Lean version %q", cfg.LeanVersion)
	}
	versions, err := cfg.AvailableLeanVersions(context.Background())
	if err != nil || len(versions) == 0 {
		t.Fatalf("AvailableLeanVersions = %v, %v", versions, err)
	}
	t.Logf("Lean %s, %d versions available (%s … %s)", cfg.LeanVersion, len(versions), versions[0], versions[len(versions)-1])
}

func TestE2ECommandAndTactic(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	srv, err := NewAutoServer(cfg, AutoServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()

	r, err := srv.RunCommand(ctx, Command{Cmd: "def x := 42"})
	if err != nil || r.Env != 0 || len(r.Messages) != 0 {
		t.Fatalf("def x: %+v, %v", r, err)
	}

	r, err = srv.RunCommand(ctx, Command{Cmd: "theorem zero_eq_zero : 0 = 0 := sorry"}, AddToSessionCache())
	if err != nil {
		t.Fatal(err)
	}
	if r.Env != -1 || len(r.Messages) != 1 || len(r.Sorries) != 1 {
		t.Fatalf("sorry response: %+v", r)
	}
	m, s := r.Messages[0], r.Sorries[0]
	if m.Severity != SeverityWarning || m.StartPos != (Pos{1, 8}) || *m.EndPos != (Pos{1, 20}) ||
		!strings.HasPrefix(m.Data, "declaration uses") {
		t.Fatalf("message: %+v", m)
	}
	if s.Goal != "⊢ 0 = 0" || *s.StartPos != (Pos{1, 32}) || *s.EndPos != (Pos{1, 37}) || s.ProofState == nil {
		t.Fatalf("sorry: %+v", s)
	}
	if !r.LeanCodeIsValid(nil, nil, true) || r.LeanCodeIsValid(nil, nil, false) {
		t.Fatal("LeanCodeIsValid mismatch")
	}

	step, err := srv.RunProofStep(ctx, ProofStep{ProofState: *s.ProofState, Tactic: "rfl"})
	if err != nil || step.ProofStatus != "Completed" || len(step.Goals) != 0 {
		t.Fatalf("proof step: %+v, %v", step, err)
	}

	r, err = srv.RunCommand(ctx, Command{Cmd: "theorem seq : (x : Nat) -> x = x := sorry"})
	if err != nil {
		t.Fatal(err)
	}
	s1, err := srv.RunProofStep(ctx, ProofStep{ProofState: *r.Sorries[0].ProofState, Tactic: "intro x"})
	if err != nil || s1.ProofStatus == "Completed" || len(s1.Goals) != 1 {
		t.Fatalf("intro x: %+v, %v", s1, err)
	}
	s2, err := srv.RunProofStep(ctx, ProofStep{ProofState: s1.ProofState, Tactic: "rfl"})
	if err != nil || s2.ProofStatus != "Completed" {
		t.Fatalf("rfl: %+v, %v", s2, err)
	}

	r, err = srv.RunCommand(ctx, Command{Cmd: "example : 1 = 2 := rfl"})
	if err != nil || !r.HasErrors() || len(r.GetErrors()) == 0 || r.LeanCodeIsValid(nil, nil, true) {
		t.Fatalf("type error expected: %+v, %v", r, err)
	}
}

func TestE2EFileNotFound(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	_, err = srv.RunCommand(ctx, FileCommand{Path: "nonexistent_file.lean"})
	le, ok := AsLeanError(err)
	if !ok {
		t.Fatalf("want LeanError, got %v", err)
	}
	if !strings.Contains(strings.ToLower(le.Message), "no such file or directory") ||
		!strings.Contains(le.Message, "file: nonexistent_file.lean") {
		t.Fatalf("message: %q", le.Message)
	}
	if !srv.IsAlive() {
		t.Fatal("server should survive a LeanError")
	}
}

func TestE2EDeclarations(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	r, err := srv.RunCommand(ctx, Command{Cmd: "variable (p : Prop)\ntheorem test (h : p) : 0 = 0 := by rfl",
		CommandOptions: CommandOptions{Declarations: true}})
	if err != nil || len(r.Declarations) != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
	d := r.Declarations[0]
	if d.FullName != "test" || d.Kind != "theorem" || d.Signature.PP != "(h : p) : 0 = 0" ||
		d.Type == nil || d.Type.PP != "0 = 0" || d.Binders == nil || len(d.Binders.Map) != 1 ||
		d.Binders.Map[0].ID != "h" || d.Value == nil || d.Value.PP != ":= by rfl" ||
		len(d.Scope.VarDecls) != 1 || d.Scope.VarDecls[0] != "variable (p : Prop)" ||
		d.Modifiers.Visibility != "regular" || d.Modifiers.RecKind != "default" {
		t.Fatalf("declaration: %+v", d)
	}
	if d.Range.Start != (Pos{2, 0}) || d.Range.Finish != (Pos{2, 38}) {
		t.Fatalf("range: %+v", d.Range)
	}
}

func TestE2EInfoTree(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	for _, opt := range []string{InfoTreeFull, InfoTreeTactics, InfoTreeOriginal, InfoTreeSubstantive} {
		r, err := srv.RunCommand(ctx, Command{Cmd: "theorem infotree_test : 0 = 0 := by rfl",
			CommandOptions: CommandOptions{InfoTree: opt}})
		if err != nil || r.InfoTree == nil {
			t.Fatalf("infotree=%s: %+v, %v", opt, r, err)
		}
	}
	r, err := srv.RunCommand(ctx, Command{Cmd: "theorem infotree_test : 0 = 0 := by rfl",
		CommandOptions: CommandOptions{InfoTree: "invalid"}})
	if err != nil || r.InfoTree != nil {
		t.Fatalf("invalid infotree: %+v, %v", r.InfoTree, err)
	}

	count := func(seq func(func(*InfoTree) bool)) int {
		n := 0
		for range seq {
			n++
		}
		return n
	}
	r, err = srv.RunCommand(ctx, Command{Cmd: "variable (p : Prop)\ntheorem infotree_test (h : p) : 0 = 0 := by rfl",
		CommandOptions: CommandOptions{InfoTree: InfoTreeFull}})
	if err != nil || len(r.InfoTree) < 2 {
		t.Fatalf("%+v, %v", r, err)
	}
	if count(r.InfoTree[0].Variables()) != 1 || count(r.InfoTree[1].Theorems()) != 1 {
		t.Fatal("variables/theorems count mismatch")
	}

	r, err = srv.RunCommand(ctx, Command{Cmd: "theorem infotree_test : 0 = 0 := by sorry",
		CommandOptions: CommandOptions{InfoTree: InfoTreeFull}})
	if err != nil || len(r.Sorries) != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
	if count(r.InfoTree[0].Commands()) != 1 || count(r.InfoTree[0].Theorems()) != 1 ||
		r.InfoTree[0].TheoremForSorry(r.Sorries[0]) == nil {
		t.Fatal("theorem for sorry not found")
	}

	r, err = srv.RunCommand(ctx, Command{Cmd: "theorem t2 (n : Nat) : n + 0 = n := by\n  simp",
		CommandOptions: CommandOptions{AllTactics: true, RootGoals: true}})
	if err != nil || len(r.Tactics) == 0 {
		t.Fatalf("tactics: %+v, %v", r, err)
	}
}

func TestE2ESessionCacheRestart(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	for name, cache := range map[string]SessionCache{
		"replay-lazy":  NewReplaySessionCache(true),
		"replay-eager": NewReplaySessionCache(false),
		"pickle":       NewPickleSessionCache(t.TempDir()),
	} {
		t.Run(name, func(t *testing.T) {
			srv, err := NewAutoServer(cfg, AutoServerOptions{SessionCache: cache})
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Kill()
			base, err := srv.RunCommand(ctx, Command{Cmd: "def secret := 1234"}, AddToSessionCache())
			if err != nil || base.Env != -1 {
				t.Fatalf("%+v, %v", base, err)
			}
			thm, err := srv.RunCommand(ctx, Command{Cmd: "theorem s : secret = 1234 := sorry", Env: Ptr(base.Env)}, AddToSessionCache())
			if err != nil {
				t.Fatal(err)
			}
			ps, err := srv.RunProofStep(ctx, ProofStep{ProofState: *thm.Sorries[0].ProofState, Tactic: "skip"}, AddToSessionCache())
			if err != nil || ps.ProofState >= 0 {
				t.Fatalf("cached proof step: %+v, %v", ps, err)
			}
			if err := srv.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := srv.RunCommand(ctx, Command{Cmd: "example : secret = 1234 := rfl", Env: Ptr(base.Env)})
			if err != nil || r.HasErrors() {
				t.Fatalf("after restart: %+v, %v", r, err)
			}
			done, err := srv.RunProofStep(ctx, ProofStep{ProofState: ps.ProofState, Tactic: "rfl"})
			if err != nil || done.ProofStatus != "Completed" {
				t.Fatalf("proof step from cached state after restart: %+v, %v", done, err)
			}
			if err := srv.ClearSessionCache(ctx, true); err != nil || !cache.IsEmpty() {
				t.Fatalf("clear: %v", err)
			}
		})
	}
}

func TestE2ETimeoutRecovery(t *testing.T) {
	cfg := e2eConfig(t)
	srv, err := NewAutoServer(cfg, AutoServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = srv.RunCommand(ctx, Command{Cmd: "def loop (n : Nat) : Nat := loop (n + 1)\npartial def f (n : Nat) : Nat := f n\n#eval f 0"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if el := time.Since(start); el > 6*time.Second {
		t.Fatalf("timeout not respected: %v", el)
	}
	if srv.IsAlive() {
		t.Fatal("server should be killed after timeout")
	}
	r, err := srv.RunCommand(e2eCtx(t), Command{Cmd: "#eval 1 + 1"})
	if err != nil || len(r.Messages) != 1 || r.Messages[0].Data != "2" {
		t.Fatalf("recovery: %+v, %v", r, err)
	}
}

func TestE2EPickle(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	dir := t.TempDir()
	r, err := srv.RunCommand(ctx, Command{Cmd: "def pickled := 7\ntheorem pt : pickled = 7 := sorry"})
	if err != nil {
		t.Fatal(err)
	}
	envFile, psFile := filepath.Join(dir, "env.olean"), filepath.Join(dir, "ps.olean")
	if _, err := srv.RunCommand(ctx, PickleEnvironment{Env: r.Env, PickleTo: envFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.RunProofStep(ctx, PickleProofState{ProofState: *r.Sorries[0].ProofState, PickleTo: psFile}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	env, err := srv.RunCommand(ctx, UnpickleEnvironment{UnpickleEnvFrom: envFile})
	if err != nil {
		t.Fatal(err)
	}
	chk, err := srv.RunCommand(ctx, Command{Cmd: "example : pickled = 7 := rfl", Env: Ptr(env.Env)})
	if err != nil || chk.HasErrors() {
		t.Fatalf("unpickled env: %+v, %v", chk, err)
	}
	ps, err := srv.RunProofStep(ctx, UnpickleProofState{UnpickleProofStateFrom: psFile, Env: Ptr(env.Env)})
	if err != nil {
		t.Fatal(err)
	}
	done, err := srv.RunProofStep(ctx, ProofStep{ProofState: ps.ProofState, Tactic: "rfl"})
	if err != nil || done.ProofStatus != "Completed" {
		t.Fatalf("unpickled proof state: %+v, %v", done, err)
	}
	if _, err := srv.RunCommand(ctx, PickleEnvironment{Env: 9999, PickleTo: filepath.Join(dir, "bad.olean")}); err == nil {
		t.Fatal("pickling an unknown env should fail")
	}
}

func TestE2EPool(t *testing.T) {
	cfg, ctx := e2eConfig(t), e2eCtx(t)
	pool, err := NewPool(cfg, PoolOptions{NumWorkers: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	base, err := pool.RunCommand(ctx, Command{Cmd: "def shared := 5"})
	if err != nil || base.Env >= 0 {
		t.Fatalf("%+v, %v", base, err)
	}
	var reqs []Request
	for i := range 12 {
		reqs = append(reqs, Command{Cmd: "example : shared + " + itoa(i) + " = " + itoa(5+i) + " := rfl", Env: Ptr(base.Env)})
	}
	reqs = append(reqs, Command{Cmd: "example : shared = 6 := rfl", Env: Ptr(base.Env)})
	results := pool.RunBatch(ctx, reqs, BatchOptions{TimeoutPerRequest: 2 * time.Minute})
	for i, res := range results {
		if res.Err != nil {
			t.Fatalf("request %d: %v", i, res.Err)
		}
		bad := res.Response.(*CommandResponse).HasErrors()
		if bad != (i == len(reqs)-1) {
			t.Fatalf("request %d: HasErrors=%v", i, bad)
		}
	}

	// Sorry proof states come back as session ids usable on every worker.
	thm, err := pool.RunCommand(ctx, Command{Cmd: "theorem p (n : Nat) : shared + n = n + 5 := sorry", Env: Ptr(base.Env)})
	if err != nil || len(thm.Sorries) != 1 || *thm.Sorries[0].ProofState >= 0 {
		t.Fatalf("%+v, %v", thm, err)
	}
	steps := make([]Request, 6)
	for i := range steps {
		steps[i] = ProofStep{ProofState: *thm.Sorries[0].ProofState, Tactic: "(unfold shared; omega)"}
	}
	for i, res := range pool.RunBatch(ctx, steps, BatchOptions{}) {
		if res.Err != nil || res.Response.(*ProofStepResponse).ProofStatus != "Completed" {
			t.Fatalf("step %d: %+v, %v", i, res.Response, res.Err)
		}
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
