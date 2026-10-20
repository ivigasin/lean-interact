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
	"sync"
	"sync/atomic"
	"time"
)

// SessionState is a snapshot of one session cache entry.
type SessionState struct {
	// SessionID is the negative id handed to users.
	SessionID int
	// IsProofState distinguishes proof states from environments.
	IsProofState bool
	// ReplIDs maps a server ID to the REPL-side id of the state on that
	// server (nil when not materialized there).
	ReplIDs map[string]*int
}

// SessionCache stores environments and proof states so that [AutoServer]
// can restore them after restarts. Implementations are safe for concurrent
// use and may be shared by several servers.
type SessionCache interface {
	// Add stores the state produced by req/resp on r and returns its new
	// negative session id.
	Add(ctx context.Context, r Runner, req Request, resp Response, verbose bool) (int, error)
	// Remove drops a session state.
	Remove(sessionStateID int) error
	// Reload is called after r restarted; it invalidates (and, for eager
	// caches, restores) r's REPL ids. timeoutPerState <= 0 means no limit.
	Reload(ctx context.Context, r Runner, timeoutPerState time.Duration, verbose bool) error
	// IsEmpty reports whether the cache holds no state.
	IsEmpty() bool
	// Clear removes all states.
	Clear() error
	// Contains reports whether the session id is cached.
	Contains(sessionStateID int) bool
	// Get returns a snapshot of a session state.
	Get(sessionStateID int) (SessionState, bool)
	// Keys returns all session ids.
	Keys() []int
	// States returns snapshots of all session states.
	States() []SessionState
	// GetReplID returns the REPL id of a session state on r, materializing
	// it if the cache supports it.
	GetReplID(ctx context.Context, sessionStateID int, r Runner) (*int, error)
}

type cacheEntry struct {
	SessionState
	request       Request // replay cache
	pickleFile    string  // pickle cache
	materializing map[string]bool
	// sorries maps a server ID to the proof states of the sorries in the
	// command response that produced this environment (replay cache).
	sorries map[string][]*int
	// sorryOf marks a state that is a sorry of another entry; it has no
	// request and is materialized together with its parent.
	sorryOf *sorryRef
	// inputSorry is set when request starts from a sorry of another entry:
	// its proof state is substituted on replay.
	inputSorry *sorryRef
}

// sorryRef designates sorry number index of the entry with session id parent.
type sorryRef struct{ parent, index int }

func (e *cacheEntry) snapshot() SessionState {
	ids := make(map[string]*int, len(e.ReplIDs))
	for k, v := range e.ReplIDs {
		if v != nil {
			v = Ptr(*v)
		}
		ids[k] = v
	}
	return SessionState{SessionID: e.SessionID, IsProofState: e.IsProofState, ReplIDs: ids}
}

// baseCache holds the bookkeeping shared by both caches.
type baseCache struct {
	mu      sync.Mutex
	entries map[int]*cacheEntry
	counter int
}

func (c *baseCache) init() { c.entries = map[int]*cacheEntry{} }

func (c *baseCache) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter--
	return c.counter
}

func (c *baseCache) replID(e *cacheEntry, r Runner) *int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return e.ReplIDs[r.ID()]
}

func (c *baseCache) setReplID(e *cacheEntry, r Runner, id *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.ReplIDs[r.ID()] = id
}

func (c *baseCache) entry(id int) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	return e, ok
}

func (c *baseCache) snapshotEntries() []*cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*cacheEntry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	// Oldest first (ids decrease over time), so dependencies replay first.
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID > out[j].SessionID })
	return out
}

func (c *baseCache) IsEmpty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries) == 0
}

func (c *baseCache) Contains(id int) bool {
	_, ok := c.entry(id)
	return ok
}

func (c *baseCache) Get(id int) (SessionState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return SessionState{}, false
	}
	return e.snapshot(), true
}

func (c *baseCache) Keys() []int {
	var keys []int
	for _, e := range c.snapshotEntries() {
		keys = append(keys, e.SessionID)
	}
	return keys
}

func (c *baseCache) States() []SessionState {
	var out []SessionState
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		out = append(out, e.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID > out[j].SessionID })
	return out
}

// requestInput returns the env / proof state a request starts from, and a
// function returning a copy of the request starting from another id.
func requestInput(req Request) (id int, isProof bool, with func(int) Request, ok bool) {
	switch r := req.(type) {
	case Command:
		if r.Env != nil {
			return *r.Env, false, func(v int) Request { r.Env = Ptr(v); return r }, true
		}
	case FileCommand:
		if r.Env != nil {
			return *r.Env, false, func(v int) Request { r.Env = Ptr(v); return r }, true
		}
	case UnpickleProofState:
		if r.Env != nil {
			return *r.Env, false, func(v int) Request { r.Env = Ptr(v); return r }, true
		}
	case PickleEnvironment:
		return r.Env, false, func(v int) Request { r.Env = v; return r }, true
	case ProofStep:
		return r.ProofState, true, func(v int) Request { r.ProofState = v; return r }, true
	case PickleProofState:
		return r.ProofState, true, func(v int) Request { r.ProofState = v; return r }, true
	}
	return 0, false, nil, false
}

func sorryProofStates(resp Response) []*int {
	cr, ok := resp.(*CommandResponse)
	if !ok || len(cr.Sorries) == 0 {
		return nil
	}
	out := make([]*int, len(cr.Sorries))
	for i, s := range cr.Sorries {
		if s.ProofState != nil {
			out[i] = Ptr(*s.ProofState)
		}
	}
	return out
}

func stateIDOf(resp Response) (int, bool, error) {
	switch r := resp.(type) {
	case *ProofStepResponse:
		return r.ProofState, true, nil
	case *CommandResponse:
		return r.Env, false, nil
	}
	return 0, false, fmt.Errorf("leaninteract: cannot store the session state for unsupported response of type %T", resp)
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

// ReplaySessionCache restores states by replaying the request that created
// them. It is the default and most reliable cache.
type ReplaySessionCache struct {
	baseCache
	lazy bool
}

// NewReplaySessionCache creates a replay cache. When lazy is true states are
// replayed on demand the first time they are used after a restart; when
// false Reload replays everything eagerly.
func NewReplaySessionCache(lazy bool) *ReplaySessionCache {
	c := &ReplaySessionCache{lazy: lazy}
	c.init()
	return c
}

// Add implements SessionCache.
func (c *ReplaySessionCache) Add(ctx context.Context, r Runner, req Request, resp Response, verbose bool) (int, error) {
	replID, isProof, err := stateIDOf(resp)
	if err != nil {
		return 0, err
	}
	id := c.nextID()
	e := &cacheEntry{
		SessionState:  SessionState{SessionID: id, IsProofState: isProof, ReplIDs: map[string]*int{r.ID(): Ptr(replID)}},
		request:       cloneRequest(req),
		materializing: map[string]bool{},
		sorries:       map[string][]*int{r.ID(): sorryProofStates(resp)},
	}
	c.mu.Lock()
	c.resolveInput(e, r.ID())
	c.entries[id] = e
	c.mu.Unlock()
	return id, nil
}

// resolveInput rewrites a request that starts from a raw REPL id produced by
// another cached state on server key, so that it can be replayed after a
// restart (where raw ids are meaningless). Upstream LeanInteract stores the
// raw id and fails to replay such requests. Must hold c.mu.
func (c *ReplaySessionCache) resolveInput(e *cacheEntry, key string) {
	raw, isProof, with, ok := requestInput(e.request)
	if !ok || raw < 0 {
		return
	}
	for _, o := range c.entries {
		if o.IsProofState == isProof && o.ReplIDs[key] != nil && *o.ReplIDs[key] == raw {
			e.request = with(o.SessionID)
			return
		}
	}
	if !isProof {
		return
	}
	for _, o := range c.entries {
		for i, ps := range o.sorries[key] {
			if ps != nil && *ps == raw {
				e.inputSorry = &sorryRef{parent: o.SessionID, index: i}
				return
			}
		}
	}
}

// AddSorryStates adds the proof states of the sorries of the cached
// environment parent, as produced on r, and returns their session ids (0 for
// a sorry without proof state). [Pool] uses it so that sorry proof states
// returned to callers are valid on every worker.
func (c *ReplaySessionCache) AddSorryStates(parent int, r Runner) ([]int, error) {
	c.mu.Lock()
	p, ok := c.entries[parent]
	if !ok {
		c.mu.Unlock()
		return nil, fmt.Errorf("leaninteract: unknown session state %d", parent)
	}
	sorries := p.sorries[r.ID()]
	c.mu.Unlock()
	ids := make([]int, len(sorries))
	for i, ps := range sorries {
		if ps == nil {
			continue
		}
		id := c.nextID()
		c.mu.Lock()
		c.entries[id] = &cacheEntry{
			SessionState:  SessionState{SessionID: id, IsProofState: true, ReplIDs: map[string]*int{r.ID(): Ptr(*ps)}},
			materializing: map[string]bool{},
			sorryOf:       &sorryRef{parent: parent, index: i},
		}
		c.mu.Unlock()
		ids[i] = id
	}
	return ids, nil
}

// sorryState materializes ref's parent on r and returns the REPL proof state
// of the referenced sorry.
func (c *ReplaySessionCache) sorryState(ctx context.Context, r Runner, ref *sorryRef) (int, error) {
	if _, err := c.GetReplID(ctx, ref.parent, r); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var ps *int
	if p, ok := c.entries[ref.parent]; ok && ref.index < len(p.sorries[r.ID()]) {
		ps = p.sorries[r.ID()][ref.index]
	}
	if ps == nil {
		return 0, fmt.Errorf("leaninteract: could not replay the cached state; sorry %d of session state %d has no proof state", ref.index, ref.parent)
	}
	return *ps, nil
}

// Remove implements SessionCache.
func (c *ReplaySessionCache) Remove(id int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
	return nil
}

// Clear implements SessionCache.
func (c *ReplaySessionCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[int]*cacheEntry{}
	return nil
}

// Reload implements SessionCache.
func (c *ReplaySessionCache) Reload(ctx context.Context, r Runner, timeoutPerState time.Duration, verbose bool) error {
	entries := c.snapshotEntries()
	c.mu.Lock()
	for _, e := range entries {
		e.ReplIDs[r.ID()] = nil
		delete(e.sorries, r.ID())
	}
	c.mu.Unlock()
	if c.lazy {
		return nil
	}
	for _, e := range entries {
		if err := c.materialize(ctx, r, e, timeoutPerState, verbose); err != nil {
			return err
		}
	}
	return nil
}

// IsMaterialized reports whether a state currently has a REPL id on r.
func (c *ReplaySessionCache) IsMaterialized(id int, r Runner) bool {
	e, ok := c.entry(id)
	return ok && c.replID(e, r) != nil
}

func (c *ReplaySessionCache) materialize(ctx context.Context, r Runner, e *cacheEntry, timeout time.Duration, verbose bool) error {
	key := r.ID()
	c.mu.Lock()
	if e.ReplIDs[key] != nil {
		c.mu.Unlock()
		return nil
	}
	if e.materializing[key] {
		c.mu.Unlock()
		return fmt.Errorf("leaninteract: session state %d is already being materialized", e.SessionID)
	}
	e.materializing[key] = true
	req, sorryOf, inputSorry := e.request, e.sorryOf, e.inputSorry
	c.mu.Unlock()
	done := func() {
		c.mu.Lock()
		delete(e.materializing, key)
		c.mu.Unlock()
	}

	if sorryOf != nil {
		ps, err := c.sorryState(ctx, r, sorryOf)
		done()
		if err != nil {
			return err
		}
		c.setReplID(e, r, Ptr(ps))
		return nil
	}
	if inputSorry != nil {
		ps, err := c.sorryState(ctx, r, inputSorry)
		if err != nil {
			done()
			return err
		}
		_, _, with, _ := requestInput(req)
		req = with(ps)
	}

	rctx, cancel := withTimeout(ctx, timeout)
	var opts []RunOption
	if verbose {
		opts = append(opts, WithVerbose())
	}
	resp, err := r.Run(rctx, req, opts...)
	cancel()
	done()

	if le, ok := AsLeanError(err); ok {
		return fmt.Errorf("leaninteract: could not replay the cached state; the Lean server returned an error: %s", le.Message)
	}
	if err != nil {
		return err
	}
	replID, isProof, err := stateIDOf(resp)
	if err != nil || isProof != e.IsProofState {
		return errors.New("leaninteract: could not replay the cached state; the Lean server returned an unexpected response")
	}
	c.mu.Lock()
	e.ReplIDs[key] = Ptr(replID)
	e.sorries[key] = sorryProofStates(resp)
	c.mu.Unlock()
	return nil
}

// GetReplID implements SessionCache; it replays the state if needed.
func (c *ReplaySessionCache) GetReplID(ctx context.Context, id int, r Runner) (*int, error) {
	e, ok := c.entry(id)
	if !ok {
		return nil, fmt.Errorf("leaninteract: unknown session state %d", id)
	}
	if v := c.replID(e, r); v != nil {
		return v, nil
	}
	if err := c.materialize(ctx, r, e, 0, false); err != nil {
		return nil, err
	}
	return c.replID(e, r), nil
}

// PickleSessionCache stores states on disk with the REPL pickle feature and
// unpickles them after restarts.
//
// Pickled Lean states are not fully reliable yet; prefer ReplaySessionCache
// unless you explicitly need serialized states.
type PickleSessionCache struct {
	baseCache
	workingDir string
}

var pickleSeq atomic.Uint64

// NewPickleSessionCache stores pickles under workingDir/session_cache.
func NewPickleSessionCache(workingDir string) *PickleSessionCache {
	c := &PickleSessionCache{workingDir: workingDir}
	c.init()
	return c
}

// Add implements SessionCache: it pickles the state to disk.
func (c *PickleSessionCache) Add(ctx context.Context, r Runner, req Request, resp Response, verbose bool) (int, error) {
	id := c.nextID()
	key := fmt.Sprintf("request_%T_%d_%d", req, time.Now().UnixNano(), pickleSeq.Add(1))
	sum := sha256.Sum256([]byte(key))
	file := filepath.Join(c.workingDir, "session_cache", fmt.Sprintf("%s_%d.olean", hex.EncodeToString(sum[:]), os.Getpid()))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return 0, err
	}
	replID, isProof, err := stateIDOf(resp)
	if err != nil {
		return 0, err
	}
	var pickle Request = PickleEnvironment{Env: replID, PickleTo: file}
	if isProof {
		pickle = PickleProofState{ProofState: replID, PickleTo: file}
	}
	unlock, err := acquireFileLock(ctx, file+".lock", 60*time.Second)
	if err != nil {
		return 0, err
	}
	defer unlock()
	var opts []RunOption
	if verbose {
		opts = append(opts, WithVerbose())
	}
	if _, err := r.Run(ctx, pickle, opts...); err != nil {
		if le, ok := AsLeanError(err); ok {
			return 0, fmt.Errorf("leaninteract: could not store the result in the session cache; the Lean server returned an error: %s", le.Message)
		}
		return 0, err
	}
	e := &cacheEntry{
		SessionState: SessionState{SessionID: id, IsProofState: isProof, ReplIDs: map[string]*int{r.ID(): Ptr(replID)}},
		pickleFile:   file,
	}
	c.mu.Lock()
	c.entries[id] = e
	c.mu.Unlock()
	return id, nil
}

// PickleFile returns the pickle path of a session state.
func (c *PickleSessionCache) PickleFile(id int) (string, bool) {
	e, ok := c.entry(id)
	if !ok {
		return "", false
	}
	return e.pickleFile, true
}

// Remove implements SessionCache: it also deletes the pickle file.
func (c *PickleSessionCache) Remove(id int) error {
	c.mu.Lock()
	e, ok := c.entries[id]
	delete(c.entries, id)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	unlock, err := acquireFileLock(context.Background(), e.pickleFile+".lock", 60*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(e.pickleFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Clear implements SessionCache.
func (c *PickleSessionCache) Clear() error {
	var errs []error
	for _, e := range c.snapshotEntries() {
		errs = append(errs, c.Remove(e.SessionID))
	}
	return errors.Join(errs...)
}

// Reload implements SessionCache: it unpickles every state into r.
func (c *PickleSessionCache) Reload(ctx context.Context, r Runner, timeoutPerState time.Duration, verbose bool) error {
	for _, e := range c.snapshotEntries() {
		if err := c.reloadOne(ctx, r, e, timeoutPerState, verbose); err != nil {
			return err
		}
	}
	return nil
}

func (c *PickleSessionCache) reloadOne(ctx context.Context, r Runner, e *cacheEntry, timeout time.Duration, verbose bool) error {
	unlock, err := acquireFileLock(ctx, e.pickleFile+".lock", timeout)
	if err != nil {
		return err
	}
	defer unlock()
	var req Request = UnpickleEnvironment{UnpickleEnvFrom: e.pickleFile}
	if e.IsProofState {
		req = UnpickleProofState{UnpickleProofStateFrom: e.pickleFile}
	}
	rctx, cancel := withTimeout(ctx, timeout)
	defer cancel()
	var opts []RunOption
	if verbose {
		opts = append(opts, WithVerbose())
	}
	resp, err := r.Run(rctx, req, opts...)
	if le, ok := AsLeanError(err); ok {
		return fmt.Errorf("leaninteract: could not reload the session cache; the Lean server returned an error: %s", le.Message)
	}
	if err != nil {
		return err
	}
	replID, _, err := stateIDOf(resp)
	if err != nil {
		return err
	}
	c.setReplID(e, r, Ptr(replID))
	return nil
}

// GetReplID implements SessionCache. Pickled states are restored by Reload,
// so this only looks the id up.
func (c *PickleSessionCache) GetReplID(_ context.Context, id int, r Runner) (*int, error) {
	e, ok := c.entry(id)
	if !ok {
		return nil, fmt.Errorf("leaninteract: unknown session state %d", id)
	}
	return c.replID(e, r), nil
}
