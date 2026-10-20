package leaninteract

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

// PoolOptions configure [NewPool].
type PoolOptions struct {
	// NumWorkers defaults to max(1, NumCPU-1).
	NumWorkers int
	// MaxTotalMemory, MaxProcessMemory and MaxRestartAttempts are passed to
	// each worker (see AutoServerOptions).
	MaxTotalMemory     float64
	MaxProcessMemory   float64
	MaxRestartAttempts int
}

// Pool distributes requests over several [AutoServer] workers sharing one
// lazy [ReplaySessionCache]. Every result is added to the shared cache, so
// returned env / proof state ids are negative session ids usable on any
// worker.
type Pool struct {
	config *Config
	// SessionCache is shared by all workers.
	SessionCache *ReplaySessionCache

	mu      sync.Mutex
	workers []*AutoServer
	free    []*AutoServer
	slots   chan struct{}
	closed  bool
}

// NewPool starts the workers concurrently.
func NewPool(config *Config, opts PoolOptions) (*Pool, error) {
	n := opts.NumWorkers
	if n <= 0 {
		n = max(1, runtime.NumCPU()-1)
	}
	p := &Pool{config: config, SessionCache: NewReplaySessionCache(true), slots: make(chan struct{}, n)}
	workers := make([]*AutoServer, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			workers[i], errs[i] = NewAutoServer(config, AutoServerOptions{
				MaxTotalMemory:     opts.MaxTotalMemory,
				MaxProcessMemory:   opts.MaxProcessMemory,
				MaxRestartAttempts: opts.MaxRestartAttempts,
				SessionCache:       p.SessionCache,
			})
		}(i)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		for _, w := range workers {
			if w != nil {
				w.Kill()
			}
		}
		return nil, err
	}
	p.workers = workers
	p.free = append([]*AutoServer(nil), workers...)
	for range workers {
		p.slots <- struct{}{}
	}
	return p, nil
}

// NumWorkers returns the number of workers.
func (p *Pool) NumWorkers() int { return cap(p.slots) }

// Close kills all workers.
func (p *Pool) Close() error {
	p.mu.Lock()
	workers := p.workers
	p.workers, p.free, p.closed = nil, nil, true
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *AutoServer) { defer wg.Done(); w.Kill() }(w)
	}
	wg.Wait()
	return nil
}

// acquire blocks until a worker is free, preferring one on which the
// requested session state is already materialized.
func (p *Pool) acquire(ctx context.Context, sessionID *int) (*AutoServer, error) {
	select {
	case <-p.slots:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		p.slots <- struct{}{}
		return nil, errors.New("leaninteract: pool is closed")
	}
	if sessionID != nil && *sessionID < 0 {
		for i, w := range p.free {
			if p.SessionCache.IsMaterialized(*sessionID, w) {
				p.free = append(p.free[:i], p.free[i+1:]...)
				return w, nil
			}
		}
	}
	w := p.free[len(p.free)-1]
	p.free = p.free[:len(p.free)-1]
	return w, nil
}

func (p *Pool) release(w *AutoServer) {
	p.mu.Lock()
	if !p.closed {
		p.free = append(p.free, w)
	}
	p.mu.Unlock()
	p.slots <- struct{}{}
}

// Run executes a request on an available worker and adds the result to the
// shared session cache.
func (p *Pool) Run(ctx context.Context, req Request, opts ...RunOption) (Response, error) {
	var sid *int
	switch r := req.(type) {
	case Command:
		sid = r.Env
	case ProofStep:
		sid = Ptr(r.ProofState)
	}
	w, err := p.acquire(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer p.release(w)
	resp, err := w.Run(ctx, req, append(opts, AddToSessionCache())...)
	if cr, ok := resp.(*CommandResponse); ok && err == nil {
		// Sorry proof states are REPL ids local to w; replace them with
		// session ids so follow-up proof steps work on any worker. Upstream
		// returns the raw ids, which silently target the wrong worker.
		ids, err := p.SessionCache.AddSorryStates(cr.Env, w)
		if err != nil {
			return nil, err
		}
		for i, id := range ids {
			if id != 0 {
				cr.Sorries[i].ProofState = Ptr(id)
			}
		}
	}
	return resp, err
}

// RunCommand runs a [CommandRequest] on an available worker.
func (p *Pool) RunCommand(ctx context.Context, req CommandRequest, opts ...RunOption) (*CommandResponse, error) {
	return asCommand(p.Run(ctx, req, opts...))
}

// RunProofStep runs a [ProofStateRequest] on an available worker.
func (p *Pool) RunProofStep(ctx context.Context, req ProofStateRequest, opts ...RunOption) (*ProofStepResponse, error) {
	return asProofStep(p.Run(ctx, req, opts...))
}

// BatchResult is the outcome of one request of a batch.
type BatchResult struct {
	Response Response
	// Err is non-nil on failure; a REPL error is a *LeanError.
	Err error
}

// BatchOptions configure RunBatch.
type BatchOptions struct {
	// TimeoutPerRequest bounds each request (Python timeout_per_cmd); 0 means
	// only ctx applies.
	TimeoutPerRequest time.Duration
	// OnProgress is called after each completed request.
	OnProgress func(done, total int)
	// Verbose logs requests and outputs.
	Verbose bool
}

// RunBatch runs requests concurrently on the workers. Results are in request
// order.
func (p *Pool) RunBatch(ctx context.Context, reqs []Request, opts BatchOptions) []BatchResult {
	results := make([]BatchResult, len(reqs))
	var runOpts []RunOption
	if opts.Verbose {
		runOpts = append(runOpts, WithVerbose())
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	sem := make(chan struct{}, p.NumWorkers())
	for i, req := range reqs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, req Request) {
			defer wg.Done()
			defer func() { <-sem }()
			rctx, cancel := withTimeout(ctx, opts.TimeoutPerRequest)
			resp, err := p.Run(rctx, req, runOpts...)
			cancel()
			results[i] = BatchResult{Response: resp, Err: err}
			if opts.OnProgress != nil {
				mu.Lock()
				done++
				opts.OnProgress(done, len(reqs))
				mu.Unlock()
			}
		}(i, req)
	}
	wg.Wait()
	return results
}
