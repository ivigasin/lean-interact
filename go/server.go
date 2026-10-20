package leaninteract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors returned by servers. They are wrapped with details; test them with
// errors.Is.
var (
	// ErrServerNotRunning is returned when the REPL process is not alive.
	ErrServerNotRunning = errors.New("leaninteract: the Lean server is not running")
	// ErrTimeout is returned when the REPL did not answer before the context
	// was done. The REPL has been killed.
	ErrTimeout = errors.New("leaninteract: the Lean server did not respond in time and is now killed")
	// ErrServerClosed is returned when the REPL closed without answering.
	ErrServerClosed = errors.New("leaninteract: the Lean server closed unexpectedly")
	// ErrMemoryLimit is returned by AutoServer when memory stays too high.
	ErrMemoryLimit = errors.New("leaninteract: memory usage is too high")
	// ErrNotSupported is returned by AutoServer.RunDict.
	ErrNotSupported = errors.New("leaninteract: not supported with automated memory management; use Run, or RunDict on a Server")
)

const closedHints = "If stdout and stderr are empty or obscure, here is a list of possible reasons (not exhaustive):\n" +
	"- Not enough memory and/or compute available\n" +
	"- The cached Lean REPL is corrupted. In this case, clear the cache using `lean-interact clear-cache`.\n" +
	"- An uncaught exception in the Lean REPL"

// RunOption customizes a single Run call.
type RunOption func(*runOptions)

type runOptions struct {
	verbose           bool
	addToSessionCache bool
}

// WithVerbose logs the request and the raw REPL output.
func WithVerbose() RunOption { return func(o *runOptions) { o.verbose = true } }

// AddToSessionCache stores the resulting environment / proof state in the
// AutoServer session cache; the response then carries a negative session id
// that survives restarts. Ignored by Server.
func AddToSessionCache() RunOption { return func(o *runOptions) { o.addToSessionCache = true } }

func collectOptions(opts []RunOption) runOptions {
	var o runOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// Runner is what session caches and pools need from a server.
type Runner interface {
	// ID uniquely identifies the server within the process.
	ID() string
	Run(ctx context.Context, req Request, opts ...RunOption) (Response, error)
}

// replProcess is one running REPL instance.
type replProcess struct {
	cmd    *exec.Cmd
	stdin  *os.File
	lines  chan string   // stdout lines; closed at EOF
	done   chan struct{} // closed when the process has exited
	stderr *lockedBuffer
	extra  *lockedBuffer // stdout read outside of requests
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Keep the tail only, enough for diagnostics.
	if l.b.Len() > 1<<20 {
		l.b.Next(l.b.Len() - 1<<19)
	}
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Server is a wrapper around one Lean REPL process. It is safe for concurrent
// use; requests are serialized.
type Server struct {
	config *Config
	id     string

	reqMu sync.Mutex // one request in flight

	mu   sync.Mutex // guards proc
	proc *replProcess
}

// NewServer starts a REPL process for config.
func NewServer(config *Config) (*Server, error) {
	s, err := newServer(config)
	if err != nil {
		return nil, err
	}
	if err := s.Start(); err != nil {
		return nil, err
	}
	return s, nil
}

func newServer(config *Config) (*Server, error) {
	if !config.IsSetup() {
		return nil, errors.New("leaninteract: the Lean environment has not been set up properly")
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &Server{config: config, id: hex.EncodeToString(b)}, nil
}

// ID uniquely identifies the server.
func (s *Server) ID() string { return s.id }

// Config returns the server configuration.
func (s *Server) Config() *Config { return s.config }

// LeanVersion is the Lean version used by the REPL.
func (s *Server) LeanVersion() string { return s.config.LeanVersion }

// Start launches the REPL process (`lake env <repl>`) in the working
// directory. NewServer calls it; use it after Kill to start again.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		return nil
	}
	c := s.config
	name, args := c.LakePath, []string{"env", c.REPLBinary()}
	if c.MemoryHardLimitMB > 0 && runtime.GOOS == "linux" {
		// Equivalent of Python's preexec_fn setrlimit(RLIMIT_AS).
		args = append([]string{"-c", `ulimit -v "$0" 2>/dev/null; exec "$@"`,
			strconv.Itoa(c.MemoryHardLimitMB * 1024), name}, args...)
		name = "/bin/sh"
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = c.WorkingDir()
	setProcessGroup(cmd)

	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return err
	}
	p := &replProcess{
		cmd: cmd, stdin: inW,
		lines: make(chan string, 64), done: make(chan struct{}),
		stderr: &lockedBuffer{}, extra: &lockedBuffer{},
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, p.stderr
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return fmt.Errorf("leaninteract: the Lean server could not be started: %w", err)
	}
	go func() {
		defer outR.Close()
		defer close(p.lines)
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				p.lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	s.proc = p
	return nil
}

func (s *Server) current() *replProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc
}

// IsAlive reports whether the REPL process is running.
func (s *Server) IsAlive() bool {
	p := s.current()
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Kill terminates the REPL process tree. It is safe to call repeatedly.
func (s *Server) Kill() {
	s.mu.Lock()
	p := s.proc
	s.proc = nil
	s.mu.Unlock()
	if p == nil {
		return
	}
	pid := p.cmd.Process.Pid
	terminateTree(pid)
	select {
	case <-p.done:
	case <-time.After(time.Second):
	}
	killTree(pid)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(time.Second):
	}
	p.stdin.Close()
}

// Close kills the REPL. It implements io.Closer.
func (s *Server) Close() error {
	s.Kill()
	return nil
}

// Restart kills and starts the REPL.
func (s *Server) Restart() error {
	s.Kill()
	return s.Start()
}

// StdoutStderr returns REPL output that was not part of a response, for
// diagnostics.
func (s *Server) StdoutStderr() (stdout, stderr string) {
	p := s.current()
	if p == nil {
		return "", ""
	}
	return outputOf(p)
}

func outputOf(p *replProcess) (string, string) {
	// Drain whatever stdout is already buffered.
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				return p.extra.String(), p.stderr.String()
			}
			p.extra.Write([]byte(l))
		default:
			return p.extra.String(), p.stderr.String()
		}
	}
}

// MemoryUsage returns the resident memory of the REPL process tree in MB.
func (s *Server) MemoryUsage() float64 {
	p := s.current()
	if p == nil {
		return 0
	}
	return float64(processTreeRSS(p.cmd.Process.Pid)) / (1024 * 1024)
}

func (s *Server) pid() int {
	if p := s.current(); p != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func diagnostics(stdout, stderr string) string {
	sep := strings.Repeat("-", 50)
	return fmt.Sprintf("\n%s\nstdout:\n%s\n%s\nstderr:\n%s\n%s", sep, stdout, sep, stderr, sep)
}

// exchange sends one JSON query and reads the answer up to the blank-line
// delimiter. On ctx expiry the REPL is killed.
func (s *Server) exchange(ctx context.Context, query []byte, verbose bool) ([]byte, error) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()

	p := s.current()
	if p == nil || !s.IsAlive() {
		var out, errOut string
		if p != nil {
			out, errOut = outputOf(p)
		}
		return nil, fmt.Errorf("%w%s", ErrServerNotRunning, diagnostics(out, errOut))
	}
	if verbose {
		Logger.Info("sending query", "query", string(query))
	}
	// Write asynchronously so a REPL that stops reading cannot block us past
	// the context deadline.
	written := make(chan error, 1)
	go func() {
		_, err := p.stdin.Write(append(query, '\n', '\n'))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			out, errOut := outputOf(p)
			s.Kill()
			return nil, fmt.Errorf("%w: %v%s\n%s", ErrServerClosed, err, diagnostics(out, errOut), closedHints)
		}
	case <-ctx.Done():
		s.Kill()
		return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
	}
	var output strings.Builder
	for {
		select {
		case line, ok := <-p.lines:
			if ok {
				output.WriteString(line)
				if !strings.HasSuffix(output.String(), "\n\n") {
					continue
				}
			}
			if strings.TrimSpace(output.String()) == "" {
				// Give the process a moment to exit so stderr is complete.
				select {
				case <-p.done:
				case <-time.After(100 * time.Millisecond):
				}
				out, errOut := outputOf(p)
				s.Kill()
				return nil, fmt.Errorf("%w: the Lean server returned no output%s\n%s", ErrServerClosed, diagnostics(out, errOut), closedHints)
			}
			if verbose {
				Logger.Info("server output", "output", output.String())
			}
			return []byte(output.String()), nil
		case <-ctx.Done():
			s.Kill()
			return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
		}
	}
}

func marshalQuery(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// RunDict sends a raw JSON request and returns the raw decoded answer. Use it
// with custom REPLs whose interface differs from the standard one.
func (s *Server) RunDict(ctx context.Context, request map[string]any, opts ...RunOption) (map[string]any, error) {
	raw, err := s.runRaw(ctx, request, collectOptions(opts).verbose)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("leaninteract: could not parse the Lean server output %q: %w", raw, err)
	}
	return out, nil
}

func (s *Server) runRaw(ctx context.Context, request any, verbose bool) ([]byte, error) {
	query, err := marshalQuery(request)
	if err != nil {
		return nil, err
	}
	return s.exchange(ctx, query, verbose)
}

// augmentRequest applies the config-wide incrementality and parallel
// elaboration settings to Command and FileCommand requests.
func augmentRequest(c *Config, req Request) Request {
	apply := func(o *CommandOptions) {
		if c.EnableIncrementalOptimization {
			o.Incrementality = true
		}
		if c.EnableParallelElaboration {
			for _, opt := range o.SetOptions {
				if len(opt.Name) == 2 && opt.Name[0] == "Elab" && opt.Name[1] == "async" {
					return
				}
			}
			o.SetOptions = append(append(Options(nil), o.SetOptions...), Option{Name: Name{"Elab", "async"}, Value: true})
		}
	}
	switch r := req.(type) {
	case Command:
		apply(&r.CommandOptions)
		return r
	case *Command:
		cp := *r
		apply(&cp.CommandOptions)
		return cp
	case FileCommand:
		apply(&r.CommandOptions)
		return r
	case *FileCommand:
		cp := *r
		apply(&cp.CommandOptions)
		return cp
	}
	return req
}

// Run executes a request. The response is a *CommandResponse or a
// *ProofStepResponse depending on the request type; a REPL error is returned
// as a *LeanError.
//
// The context bounds the request: if it is done before the REPL answers, the
// REPL is killed (a half-read answer would desynchronize the protocol).
func (s *Server) Run(ctx context.Context, req Request, opts ...RunOption) (Response, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	req = augmentRequest(s.config, req)
	raw, err := s.runRaw(ctx, req, collectOptions(opts).verbose)
	if err != nil {
		return nil, err
	}
	return decodeResponse(raw, req.responseKind(), false)
}

// RunCommand runs a [CommandRequest] and returns its typed response.
func (s *Server) RunCommand(ctx context.Context, req CommandRequest, opts ...RunOption) (*CommandResponse, error) {
	return asCommand(s.Run(ctx, req, opts...))
}

// RunProofStep runs a [ProofStateRequest] and returns its typed response.
func (s *Server) RunProofStep(ctx context.Context, req ProofStateRequest, opts ...RunOption) (*ProofStepResponse, error) {
	return asProofStep(s.Run(ctx, req, opts...))
}

func asCommand(r Response, err error) (*CommandResponse, error) {
	if err != nil {
		return nil, err
	}
	return r.(*CommandResponse), nil
}

func asProofStep(r Response, err error) (*ProofStepResponse, error) {
	if err != nil {
		return nil, err
	}
	return r.(*ProofStepResponse), nil
}
