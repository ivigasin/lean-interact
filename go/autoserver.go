package leaninteract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AutoServerOptions configure [NewAutoServer]. Zero values select the Python
// defaults.
type AutoServerOptions struct {
	// MaxTotalMemory is the share of system memory in (0, 1] above which the
	// REPL is restarted before a request. Default 0.8.
	MaxTotalMemory float64
	// MaxProcessMemory is the share of Config.MemoryHardLimitMB the REPL tree
	// may use before being restarted. Default 0.8; negative disables the
	// check. Only applies when a hard limit is configured.
	MaxProcessMemory float64
	// MaxRestartAttempts is the number of consecutive memory-triggered
	// restarts (with 2^n s exponential backoff) before ErrMemoryLimit.
	// Default 5; negative means 0.
	MaxRestartAttempts int
	// SessionCache keeps states across restarts. Defaults to a lazy
	// [ReplaySessionCache].
	SessionCache SessionCache
}

// AutoServer is a [Server] that restarts the REPL automatically when memory
// is short, after timeouts and after crashes, and restores the states stored
// in its session cache. Session states have negative ids (-1, -2, ...) that
// stay valid across restarts and are translated to REPL ids transparently.
type AutoServer struct {
	*Server

	cache              SessionCache
	maxTotalMemory     float64
	maxProcessMemory   float64
	maxRestartAttempts int

	// Hooks for tests.
	sysMemPercent func() float64
	backoff       func(attempt int) time.Duration
}

// NewAutoServer starts an auto-restarting server.
func NewAutoServer(config *Config, opts AutoServerOptions) (*AutoServer, error) {
	s, err := newServer(config)
	if err != nil {
		return nil, err
	}
	a := &AutoServer{
		Server:             s,
		cache:              opts.SessionCache,
		maxTotalMemory:     opts.MaxTotalMemory,
		maxProcessMemory:   opts.MaxProcessMemory,
		maxRestartAttempts: opts.MaxRestartAttempts,
		sysMemPercent:      systemMemoryPercent,
		backoff:            func(n int) time.Duration { return time.Duration(1<<n) * time.Second },
	}
	if a.cache == nil {
		a.cache = NewReplaySessionCache(true)
	}
	if a.maxTotalMemory == 0 {
		a.maxTotalMemory = 0.8
	}
	if a.maxProcessMemory == 0 {
		a.maxProcessMemory = 0.8
	}
	if a.maxRestartAttempts == 0 {
		a.maxRestartAttempts = 5
	} else if a.maxRestartAttempts < 0 {
		a.maxRestartAttempts = 0
	}
	if err := a.Start(); err != nil {
		return nil, err
	}
	return a, nil
}

// SessionCache returns the session cache.
func (a *AutoServer) SessionCache() SessionCache { return a.cache }

// Restart restarts the REPL and reloads the session cache.
func (a *AutoServer) Restart(ctx context.Context) error {
	if err := a.Server.Restart(); err != nil {
		return err
	}
	return a.cache.Reload(ctx, a, 0, false)
}

// RemoveFromSessionCache removes a session state.
func (a *AutoServer) RemoveFromSessionCache(sessionStateID int) error {
	return a.cache.Remove(sessionStateID)
}

// ClearSessionCache clears the session cache. With force the REPL is
// restarted immediately to free memory; otherwise memory is reclaimed at the
// next automatic restart.
func (a *AutoServer) ClearSessionCache(ctx context.Context, force bool) error {
	if err := a.cache.Clear(); err != nil {
		return err
	}
	if force {
		return a.Restart(ctx)
	}
	return nil
}

// RunDict is not available with automated memory management; it always
// returns ErrNotSupported. Use Run, or a plain [Server].
func (a *AutoServer) RunDict(context.Context, map[string]any, ...RunOption) (map[string]any, error) {
	return nil, ErrNotSupported
}

func (a *AutoServer) memoryTooHigh() bool {
	if a.sysMemPercent() >= 100*a.maxTotalMemory {
		return true
	}
	limit := a.config.MemoryHardLimitMB
	if limit <= 0 || a.maxProcessMemory < 0 || !a.IsAlive() {
		return false
	}
	return float64(processTreeRSS(a.pid())) >= a.maxProcessMemory*float64(limit)*1024*1024
}

func (a *AutoServer) translateID(ctx context.Context, m map[string]any, key string) error {
	n, ok := m[key].(json.Number)
	if !ok {
		return nil
	}
	id, err := n.Int64()
	if err != nil || id >= 0 || !a.cache.Contains(int(id)) {
		return nil
	}
	replID, err := a.cache.GetReplID(ctx, int(id), a)
	if err != nil {
		return err
	}
	if replID == nil {
		m[key] = nil
	} else {
		m[key] = *replID
	}
	return nil
}

func (a *AutoServer) runDictBackoff(ctx context.Context, request map[string]any, verbose bool, attempt int) ([]byte, error) {
	if a.memoryTooHigh() {
		a.Kill()
		if attempt >= a.maxRestartAttempts {
			return nil, fmt.Errorf("%w: attempted to restart the Lean server %d times without success", ErrMemoryLimit, a.maxRestartAttempts)
		}
		if verbose {
			Logger.Info("memory usage is too high, reloading the Lean server...")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(a.backoff(attempt)):
		}
		return a.runDictBackoff(ctx, request, verbose, attempt+1)
	}
	if !a.IsAlive() {
		a.Kill()
		if err := a.Start(); err != nil {
			return nil, err
		}
		if err := a.cache.Reload(ctx, a, 0, verbose); err != nil {
			return nil, err
		}
	}
	if err := a.translateID(ctx, request, "env"); err != nil {
		return nil, err
	}
	if err := a.translateID(ctx, request, "proofState"); err != nil {
		return nil, err
	}
	return a.runRaw(ctx, request, verbose)
}

// Run executes a request, restarting the REPL when needed. With
// [AddToSessionCache] the resulting state is stored in the session cache and
// the response carries its negative session id.
func (a *AutoServer) Run(ctx context.Context, req Request, opts ...RunOption) (Response, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	o := collectOptions(opts)
	req = augmentRequest(a.config, req)
	query, err := marshalQuery(req)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(query))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	raw, err := a.runDictBackoff(ctx, m, o.verbose, 0)
	if err != nil {
		return nil, err
	}
	resp, err := decodeResponse(raw, req.responseKind(), true)
	if err != nil || !o.addToSessionCache {
		return resp, err
	}
	id, err := a.cache.Add(ctx, a, req, resp, o.verbose)
	if err != nil {
		return nil, err
	}
	switch r := resp.(type) {
	case *CommandResponse:
		r.Env = id
	case *ProofStepResponse:
		r.ProofState = id
	}
	return resp, nil
}

// RunCommand runs a [CommandRequest] and returns its typed response.
func (a *AutoServer) RunCommand(ctx context.Context, req CommandRequest, opts ...RunOption) (*CommandResponse, error) {
	return asCommand(a.Run(ctx, req, opts...))
}

// RunProofStep runs a [ProofStateRequest] and returns its typed response.
func (a *AutoServer) RunProofStep(ctx context.Context, req ProofStateRequest, opts ...RunOption) (*ProofStepResponse, error) {
	return asProofStep(a.Run(ctx, req, opts...))
}
