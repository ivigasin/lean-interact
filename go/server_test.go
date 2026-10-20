package leaninteract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

func TestServerBasics(t *testing.T) {
	cfg := newFakeConfig(t)
	if cfg.LeanVersion != "v4.19.0" {
		t.Fatalf("lean version %q", cfg.LeanVersion)
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	if !srv.IsAlive() {
		t.Fatal("server not alive")
	}

	r, err := srv.RunCommand(bg, Command{Cmd: "theorem zero_eq_zero : 0 = 0 := sorry"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Env != 0 || len(r.Sorries) != 1 || *r.Sorries[0].ProofState != 0 || r.LeanCodeIsValid(nil, nil, false) {
		t.Fatalf("got %+v", r)
	}
	step, err := srv.RunProofStep(bg, ProofStep{ProofState: 0, Tactic: "rfl"})
	if err != nil || step.ProofStatus != "Completed" || step.ProofState != 1 {
		t.Fatalf("got %+v, %v", step, err)
	}
	r2, err := srv.RunCommand(bg, Command{Cmd: "def y := x", Env: Ptr(0)})
	if err != nil || r2.Env != 1 {
		t.Fatalf("got %+v, %v", r2, err)
	}

	_, err = srv.RunCommand(bg, Command{Cmd: "def z := 1", Env: Ptr(42)})
	if le, ok := AsLeanError(err); !ok || le.Message != "Unknown environment." {
		t.Fatalf("got %v", err)
	}
	_, err = srv.RunCommand(bg, FileCommand{Path: "nonexistent_file.lean"})
	if le, ok := AsLeanError(err); !ok || !strings.Contains(le.Message, "file: nonexistent_file.lean") {
		t.Fatalf("got %v", err)
	}
	if _, err := srv.RunCommand(bg, Command{}); err == nil {
		t.Fatal("empty command accepted")
	}
}

func echoedRequest(t *testing.T, r *CommandResponse) map[string]any {
	t.Helper()
	var req map[string]any
	if len(r.Messages) == 0 || json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Data), &req) != nil {
		t.Fatalf("no echo in %+v", r)
	}
	return req
}

func TestServerAugmentsRequests(t *testing.T) {
	cfg := newFakeConfig(t)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	r, err := srv.RunCommand(bg, Command{Cmd: "#echo"})
	if err != nil {
		t.Fatal(err)
	}
	req := echoedRequest(t, r)
	if req["incrementality"] != true {
		t.Fatalf("incrementality not set: %v", req)
	}
	if opts, _ := json.Marshal(req["setOptions"]); string(opts) != `[[["Elab","async"],true]]` {
		t.Fatalf("setOptions %s", opts)
	}

	cfg.EnableIncrementalOptimization, cfg.EnableParallelElaboration = false, false
	r, err = srv.RunCommand(bg, Command{Cmd: "#echo"})
	if err != nil {
		t.Fatal(err)
	}
	if req := echoedRequest(t, r); req["incrementality"] != nil || req["setOptions"] != nil {
		t.Fatalf("unexpected augmentation: %v", req)
	}
}

func TestServerRunDict(t *testing.T) {
	srv, err := NewServer(newFakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	out, err := srv.RunDict(bg, map[string]any{"cmd": "def x := 1"})
	if err != nil {
		t.Fatal(err)
	}
	if out["env"] != json.Number("0") {
		t.Fatalf("got %v", out)
	}
}

func TestServerTimeoutKills(t *testing.T) {
	srv, err := NewServer(newFakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	ctx, cancel := context.WithTimeout(bg, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = srv.RunCommand(ctx, Command{Cmd: "sleep"})
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not respected")
	}
	if srv.IsAlive() {
		t.Fatal("server should be killed after a timeout")
	}
	if _, err := srv.RunCommand(bg, Command{Cmd: "x"}); !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("got %v", err)
	}
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RunCommand(bg, Command{Cmd: "x"}); err != nil || r.Env != 0 {
		t.Fatalf("after restart: %+v %v", r, err)
	}
}

func TestServerCrash(t *testing.T) {
	srv, err := NewServer(newFakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	_, err = srv.RunCommand(bg, Command{Cmd: "crash"})
	if !errors.Is(err, ErrServerClosed) || !strings.Contains(err.Error(), "fake REPL crashing") {
		t.Fatalf("got %v", err)
	}
}

func TestServerConcurrentRequests(t *testing.T) {
	srv, err := NewServer(newFakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	var wg sync.WaitGroup
	envs := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := srv.RunCommand(bg, Command{Cmd: "def x := 1"})
			if err != nil {
				t.Error(err)
				return
			}
			envs <- r.Env
		}()
	}
	wg.Wait()
	close(envs)
	seen := map[int]bool{}
	for e := range envs {
		if seen[e] {
			t.Fatalf("env %d returned twice: responses got mixed up", e)
		}
		seen[e] = true
	}
	if len(seen) != 20 {
		t.Fatalf("got %d responses", len(seen))
	}
}

func TestAutoServerSessionCache(t *testing.T) {
	srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	r, err := srv.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache())
	if err != nil || r.Env != -1 {
		t.Fatalf("got %+v, %v", r, err)
	}
	r2, err := srv.RunCommand(bg, Command{Cmd: "def y := x + 1", Env: Ptr(-1)})
	if err != nil || r2.Env != 1 {
		t.Fatalf("got %+v, %v", r2, err)
	}

	// Proof states can be cached too.
	thm, err := srv.RunCommand(bg, Command{Cmd: "theorem t : 0 = 0 := sorry"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := srv.RunProofStep(bg, ProofStep{ProofState: *thm.Sorries[0].ProofState, Tactic: "intro x"}, AddToSessionCache())
	if err != nil || step.ProofState != -2 {
		t.Fatalf("got %+v, %v", step, err)
	}

	// After a restart the REPL forgot env 0, but the replay cache rebuilds it.
	if err := srv.Restart(bg); err != nil {
		t.Fatal(err)
	}
	r3, err := srv.RunCommand(bg, Command{Cmd: "def z := x", Env: Ptr(-1)})
	if err != nil {
		t.Fatalf("rehydration failed: %v", err)
	}
	if r3.Env != 1 { // replay produced env 0, then this command env 1
		t.Fatalf("got %+v", r3)
	}
	if st, _ := srv.SessionCache().Get(-1); *st.ReplIDs[srv.ID()] != 0 {
		t.Fatalf("state %+v", st)
	}

	if _, err := srv.RunDict(bg, map[string]any{"cmd": "x"}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("RunDict: %v", err)
	}
	if err := srv.ClearSessionCache(bg, true); err != nil || !srv.SessionCache().IsEmpty() {
		t.Fatalf("clear: %v", err)
	}
}

func TestAutoServerRecovers(t *testing.T) {
	srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	if _, err := srv.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
	defer cancel()
	if _, err := srv.RunCommand(ctx, Command{Cmd: "sleep"}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if srv.IsAlive() {
		t.Fatal("expected killed server")
	}
	// The next request restarts the REPL transparently.
	r, err := srv.RunCommand(bg, Command{Cmd: "def y := x", Env: Ptr(-1)})
	if err != nil || r.Env != 1 {
		t.Fatalf("after timeout: %+v, %v", r, err)
	}
	if _, err := srv.RunCommand(bg, Command{Cmd: "crash"}); !errors.Is(err, ErrServerClosed) {
		t.Fatalf("got %v", err)
	}
	if r, err := srv.RunCommand(bg, Command{Cmd: "def y := x", Env: Ptr(-1)}); err != nil || r.Env != 1 {
		t.Fatalf("after crash: %+v, %v", r, err)
	}
}

func TestAutoServerMemoryBackoff(t *testing.T) {
	srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{MaxRestartAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	var attempts []int
	srv.sysMemPercent = func() float64 { return 99 }
	srv.backoff = func(n int) time.Duration { attempts = append(attempts, n); return time.Millisecond }
	if _, err := srv.RunCommand(bg, Command{Cmd: "x"}); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("got %v", err)
	}
	if len(attempts) != 2 || attempts[1] != 1 {
		t.Fatalf("attempts %v", attempts)
	}
	// Memory pressure that resolves after one restart.
	calls := 0
	srv.sysMemPercent = func() float64 { calls++; return map[bool]float64{true: 95, false: 10}[calls == 1] }
	if r, err := srv.RunCommand(bg, Command{Cmd: "x"}); err != nil || r.Env != 0 {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestSharedReplayCacheAcrossServers(t *testing.T) {
	cfg := newFakeConfig(t)
	cache := NewReplaySessionCache(true)
	a, err := NewAutoServer(cfg, AutoServerOptions{SessionCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Kill()
	b, err := NewAutoServer(cfg, AutoServerOptions{SessionCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Kill()
	for i := 0; i < 3; i++ { // advance a's env counter
		a.RunCommand(bg, Command{Cmd: "def pad := 0"})
	}
	r, err := a.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache())
	if err != nil || r.Env != -1 {
		t.Fatal(r, err)
	}
	if cache.IsMaterialized(-1, b) {
		t.Fatal("should not be materialized on b yet")
	}
	rb, err := b.RunCommand(bg, Command{Cmd: "def y := x", Env: Ptr(-1)})
	if err != nil || rb.Env != 1 {
		t.Fatalf("got %+v, %v", rb, err)
	}
	st, _ := cache.Get(-1)
	if *st.ReplIDs[a.ID()] != 3 || *st.ReplIDs[b.ID()] != 0 {
		t.Fatalf("repl ids %+v", st.ReplIDs)
	}
}

func TestEagerReplayCache(t *testing.T) {
	cache := NewReplaySessionCache(false)
	srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{SessionCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	srv.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache())
	srv.RunCommand(bg, Command{Cmd: "def y := 1", Env: Ptr(-1)}, AddToSessionCache())
	if err := srv.Restart(bg); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{-1, -2} {
		if !cache.IsMaterialized(id, srv) {
			t.Fatalf("state %d not eagerly materialized", id)
		}
	}
	if keys := cache.Keys(); len(keys) != 2 || keys[0] != -1 {
		t.Fatalf("keys %v", keys)
	}
}

func TestPickleSessionCache(t *testing.T) {
	dir := t.TempDir()
	cache := NewPickleSessionCache(dir)
	srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{SessionCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	r, err := srv.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache())
	if err != nil || r.Env != -1 {
		t.Fatal(r, err)
	}
	file, _ := cache.PickleFile(-1)
	if _, err := os.Stat(file); err != nil || filepath.Dir(file) != filepath.Join(dir, "session_cache") {
		t.Fatalf("pickle file %q: %v", file, err)
	}
	if err := srv.Restart(bg); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RunCommand(bg, Command{Cmd: "def y := x", Env: Ptr(-1)}); err != nil || r.Env != 1 {
		t.Fatalf("after restart %+v %v", r, err)
	}
	if err := cache.Clear(); err != nil || !cache.IsEmpty() {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("pickle file not removed")
	}
}

func TestPool(t *testing.T) {
	pool, err := NewPool(newFakeConfig(t), PoolOptions{NumWorkers: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reqs := make([]Request, 10)
	for i := range reqs {
		reqs[i] = Command{Cmd: "def x := 1"}
	}
	reqs[4] = Command{Cmd: "def y := 1", Env: Ptr(1234)}
	var progress int
	results := pool.RunBatch(bg, reqs, BatchOptions{OnProgress: func(done, total int) { progress = done }})
	if progress != 10 {
		t.Fatalf("progress %d", progress)
	}
	seen := map[int]bool{}
	for i, res := range results {
		if i == 4 {
			if _, ok := AsLeanError(res.Err); !ok {
				t.Fatalf("expected LeanError, got %v", res.Err)
			}
			continue
		}
		if res.Err != nil {
			t.Fatal(res.Err)
		}
		env := res.Response.(*CommandResponse).Env
		if env >= 0 || seen[env] {
			t.Fatalf("expected unique session ids, got %d", env)
		}
		seen[env] = true
	}
	// Any worker can continue from any session id.
	for env := range seen {
		r, err := pool.RunCommand(bg, Command{Cmd: "def z := x", Env: Ptr(env)})
		if err != nil || r.Env >= 0 {
			t.Fatalf("continue from %d: %+v %v", env, r, err)
		}
	}
	ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
	defer cancel()
	timed := pool.RunBatch(ctx, []Request{Command{Cmd: "sleep"}}, BatchOptions{})
	if !errors.Is(timed[0].Err, ErrTimeout) {
		t.Fatalf("got %v", timed[0].Err)
	}
}

// A cached proof step that starts from a sorry of a cached command stores a
// raw, server-local proof state id. It must still replay after a restart
// (upstream LeanInteract fails here) and through pools.
func TestReplayCacheSorryProofStates(t *testing.T) {
	for _, lazy := range []bool{true, false} {
		srv, err := NewAutoServer(newFakeConfig(t), AutoServerOptions{SessionCache: NewReplaySessionCache(lazy)})
		if err != nil {
			t.Fatal(err)
		}
		defer srv.Kill()
		// Uncached sorries first, so the cached proof state ids are not the
		// ones a replay on a fresh REPL produces.
		for range 3 {
			if _, err := srv.RunCommand(bg, Command{Cmd: "theorem u : 0 = 0 := sorry"}); err != nil {
				t.Fatal(err)
			}
		}
		base, err := srv.RunCommand(bg, Command{Cmd: "def x := 1"}, AddToSessionCache())
		if err != nil {
			t.Fatal(err)
		}
		thm, err := srv.RunCommand(bg, Command{Cmd: "theorem t : 0 = 0 := sorry", Env: Ptr(base.Env)}, AddToSessionCache())
		if err != nil {
			t.Fatal(err)
		}
		step, err := srv.RunProofStep(bg, ProofStep{ProofState: *thm.Sorries[0].ProofState, Tactic: "intro x"}, AddToSessionCache())
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Restart(bg); err != nil {
			t.Fatal(err)
		}
		done, err := srv.RunProofStep(bg, ProofStep{ProofState: step.ProofState, Tactic: "rfl"})
		if err != nil || done.ProofStatus != "Completed" {
			t.Fatalf("lazy=%v: %+v, %v", lazy, done, err)
		}
	}

	pool, err := NewPool(newFakeConfig(t), PoolOptions{NumWorkers: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	thm, err := pool.RunCommand(bg, Command{Cmd: "theorem t : 0 = 0 := sorry"})
	if err != nil {
		t.Fatal(err)
	}
	ps := *thm.Sorries[0].ProofState
	if ps >= 0 {
		t.Fatalf("pool should return a session id for sorry proof states, got %d", ps)
	}
	reqs := make([]Request, 9)
	for i := range reqs {
		reqs[i] = ProofStep{ProofState: ps, Tactic: "rfl"}
	}
	for i, res := range pool.RunBatch(bg, reqs, BatchOptions{}) {
		if res.Err != nil || res.Response.(*ProofStepResponse).ProofStatus != "Completed" {
			t.Fatalf("step %d: %+v, %v", i, res.Response, res.Err)
		}
	}
}
