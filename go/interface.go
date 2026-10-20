package leaninteract

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Request and response types aligned with the Lean REPL JSON API
// (https://github.com/leanprover-community/repl/blob/master/REPL/JSON.lean).
// Field names on the wire are camelCase, exactly as LeanInteract sends them.

// Ptr returns a pointer to v. It is handy for optional request fields such as
// Command.Env:
//
//	leaninteract.Command{Cmd: "def y := x", Env: leaninteract.Ptr(0)}
func Ptr[T any](v T) *T { return &v }

// Name is a hierarchical Lean name, e.g. ["Elab", "async"] for Elab.async.
type Name []string

// Option is a single `set_option` entry: a name and its value. The value must
// be a bool, an integer, a string or a [Name]. It is encoded as the JSON pair
// [name, value].
type Option struct {
	Name  Name
	Value any
}

// MarshalJSON encodes the option as [name, value].
func (o Option) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{o.Name, o.Value})
}

// UnmarshalJSON decodes an option from [name, value].
func (o *Option) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("leaninteract: option must be a [name, value] pair, got %s", b)
	}
	if err := json.Unmarshal(pair[0], &o.Name); err != nil {
		return err
	}
	return json.Unmarshal(pair[1], &o.Value)
}

// Options is a list of `set_option` entries.
type Options []Option

// InfoTree detail levels accepted by CommandOptions.InfoTree.
const (
	// InfoTreeFull includes the entire InfoTree.
	InfoTreeFull = "full"
	// InfoTreeTactics keeps only the nodes produced by tactics.
	InfoTreeTactics = "tactics"
	// InfoTreeOriginal keeps tactic nodes, restricted to non-synthetic parts.
	InfoTreeOriginal = "original"
	// InfoTreeSubstantive keeps tactic nodes that are not mere combinators
	// (`by`, `;`, parentheses, ...).
	InfoTreeSubstantive = "substantive"
)

// CommandOptions are the options shared by Command and FileCommand.
type CommandOptions struct {
	// AllTactics returns all tactics used in the command with their information.
	AllTactics bool `json:"allTactics,omitempty"`
	// Declarations returns detailed information about declarations in the command.
	Declarations bool `json:"declarations,omitempty"`
	// RootGoals returns the initial goals of all declarations, even proved ones.
	RootGoals bool `json:"rootGoals,omitempty"`
	// InfoTree requests syntax information: one of the InfoTree* constants.
	// Anything else is ignored by the REPL.
	InfoTree string `json:"infotree,omitempty"`
	// Incrementality enables incremental elaboration for the command.
	Incrementality bool `json:"incrementality,omitempty"`
	// SetOptions are options set before executing the command.
	SetOptions Options `json:"setOptions,omitempty"`
}

// Request is implemented by every REPL request type.
type Request interface {
	responseKind() responseKind
	validate() error
}

// CommandRequest is a request answered with a [CommandResponse]:
// [Command], [FileCommand], [PickleEnvironment] and [UnpickleEnvironment].
type CommandRequest interface {
	Request
	isCommandRequest()
}

// ProofStateRequest is a request answered with a [ProofStepResponse]:
// [ProofStep], [PickleProofState] and [UnpickleProofState].
type ProofStateRequest interface {
	Request
	isProofStateRequest()
}

type responseKind int

const (
	kindCommand responseKind = iota
	kindProofStep
)

// Command runs Lean code in the REPL.
type Command struct {
	// Cmd is the Lean code to execute. Must not be empty.
	Cmd string `json:"cmd"`
	// Env is the environment to run in. Nil starts a fresh session (where
	// `import` is allowed).
	Env *int `json:"env,omitempty"`
	CommandOptions
}

// FileCommand runs a Lean file in the REPL.
type FileCommand struct {
	// Path is the path of the file. Must not be empty.
	Path string `json:"path"`
	// Env is the environment to run in. Nil starts a fresh session.
	Env *int `json:"env,omitempty"`
	CommandOptions
}

// ProofStep applies a tactic to a proof state.
type ProofStep struct {
	ProofState int    `json:"proofState"`
	Tactic     string `json:"tactic"`
}

// PickleEnvironment saves an environment to disk.
type PickleEnvironment struct {
	Env      int    `json:"env"`
	PickleTo string `json:"pickleTo"`
}

// UnpickleEnvironment loads an environment from disk.
type UnpickleEnvironment struct {
	UnpickleEnvFrom string `json:"unpickleEnvFrom"`
}

// PickleProofState saves a proof state to disk.
type PickleProofState struct {
	ProofState int    `json:"proofState"`
	PickleTo   string `json:"pickleTo"`
}

// UnpickleProofState loads a proof state from disk.
type UnpickleProofState struct {
	UnpickleProofStateFrom string `json:"unpickleProofStateFrom"`
	// Env is the environment used as context for unpickling.
	Env *int `json:"env,omitempty"`
}

func (Command) responseKind() responseKind             { return kindCommand }
func (FileCommand) responseKind() responseKind         { return kindCommand }
func (PickleEnvironment) responseKind() responseKind   { return kindCommand }
func (UnpickleEnvironment) responseKind() responseKind { return kindCommand }
func (ProofStep) responseKind() responseKind           { return kindProofStep }
func (PickleProofState) responseKind() responseKind    { return kindProofStep }
func (UnpickleProofState) responseKind() responseKind  { return kindProofStep }

func (Command) isCommandRequest()               {}
func (FileCommand) isCommandRequest()           {}
func (PickleEnvironment) isCommandRequest()     {}
func (UnpickleEnvironment) isCommandRequest()   {}
func (ProofStep) isProofStateRequest()          {}
func (PickleProofState) isProofStateRequest()   {}
func (UnpickleProofState) isProofStateRequest() {}

func nonEmpty(field, v string) error {
	if v == "" {
		return fmt.Errorf("leaninteract: %s must not be empty", field)
	}
	return nil
}

func (r Command) validate() error             { return nonEmpty("cmd", r.Cmd) }
func (r FileCommand) validate() error         { return nonEmpty("path", r.Path) }
func (r ProofStep) validate() error           { return nonEmpty("tactic", r.Tactic) }
func (r PickleEnvironment) validate() error   { return nonEmpty("pickleTo", r.PickleTo) }
func (r UnpickleEnvironment) validate() error { return nonEmpty("unpickleEnvFrom", r.UnpickleEnvFrom) }
func (r PickleProofState) validate() error    { return nonEmpty("pickleTo", r.PickleTo) }
func (r UnpickleProofState) validate() error {
	return nonEmpty("unpickleProofStateFrom", r.UnpickleProofStateFrom)
}

// cloneRequest returns a copy of r that shares no mutable state with it.
func cloneRequest(r Request) Request {
	cloneOpts := func(o CommandOptions) CommandOptions {
		if o.SetOptions != nil {
			o.SetOptions = append(Options(nil), o.SetOptions...)
		}
		return o
	}
	cloneInt := func(p *int) *int {
		if p == nil {
			return nil
		}
		return Ptr(*p)
	}
	switch v := r.(type) {
	case Command:
		v.Env, v.CommandOptions = cloneInt(v.Env), cloneOpts(v.CommandOptions)
		return v
	case FileCommand:
		v.Env, v.CommandOptions = cloneInt(v.Env), cloneOpts(v.CommandOptions)
		return v
	case UnpickleProofState:
		v.Env = cloneInt(v.Env)
		return v
	}
	return r
}

// Pos is a position in Lean code. Lines start at 1, columns at 0 and count
// Unicode code points.
type Pos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Compare returns -1, 0 or 1 when p is before, equal to or after q.
func (p Pos) Compare(q Pos) int {
	switch {
	case p.Line < q.Line || (p.Line == q.Line && p.Column < q.Column):
		return -1
	case p == q:
		return 0
	}
	return 1
}

// Less reports whether p is strictly before q.
func (p Pos) Less(q Pos) bool { return p.Compare(q) < 0 }

// LessEq reports whether p is before or equal to q.
func (p Pos) LessEq(q Pos) bool { return p.Compare(q) <= 0 }

// Severity of a [Message].
type Severity string

// Message severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
	SeverityTrace   Severity = "trace"
)

// Message is a diagnostic produced by Lean.
type Message struct {
	StartPos Pos      `json:"pos"`
	EndPos   *Pos     `json:"endPos,omitempty"`
	Severity Severity `json:"severity"`
	Data     string   `json:"data"`
}

// Sorry is a `sorry` found in the submitted code.
type Sorry struct {
	StartPos *Pos   `json:"pos,omitempty"`
	EndPos   *Pos   `json:"endPos,omitempty"`
	Goal     string `json:"goal"`
	// ProofState can be used with [ProofStep] to continue the proof.
	ProofState *int `json:"proofState,omitempty"`
}

// Tactic is a tactic occurrence, returned when CommandOptions.AllTactics is set.
type Tactic struct {
	StartPos      Pos      `json:"pos"`
	EndPos        Pos      `json:"endPos"`
	Goals         string   `json:"goals"`
	Tactic        string   `json:"tactic"`
	ProofState    *int     `json:"proofState,omitempty"`
	UsedConstants []string `json:"usedConstants,omitempty"`
}

func messageIntersectsCode(msgStart, msgEnd, start, end *Pos) bool {
	res := true
	if start != nil && msgEnd != nil {
		res = res && msgEnd.Line >= start.Line
	}
	if end != nil && msgStart != nil {
		res = res && msgStart.Line <= end.Line
	}
	return res
}

// Range is the source range of a syntax object.
type Range struct {
	Synthetic bool `json:"synthetic"`
	Start     Pos  `json:"start"`
	Finish    Pos  `json:"finish"`
}

// Equal compares ranges by their bounds only, ignoring Synthetic (as the
// Python implementation does).
func (r Range) Equal(o Range) bool { return r.Start == o.Start && r.Finish == o.Finish }

// DocString is a declaration docstring.
type DocString struct {
	Content string `json:"content"`
	Range   Range  `json:"range"`
}

// DeclModifiers are the modifiers of a declaration.
type DeclModifiers struct {
	DocString *DocString `json:"docString,omitempty"`
	// Visibility is "regular", "private", "protected" or "public".
	Visibility string `json:"visibility"`
	// ComputeKind is "regular", "meta" or "noncomputable".
	ComputeKind string `json:"computeKind"`
	// RecKind is "default", "partial" or "nonrec".
	RecKind     string   `json:"recKind"`
	IsProtected bool     `json:"isProtected"`
	IsUnsafe    bool     `json:"isUnsafe"`
	Attributes  []string `json:"attributes"`
}

// UnmarshalJSON applies the Python defaults for missing fields.
func (m *DeclModifiers) UnmarshalJSON(b []byte) error {
	type raw DeclModifiers
	v := raw{Visibility: "regular", ComputeKind: "regular", RecKind: "default"}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = DeclModifiers(v)
	return nil
}

// DeclSignature is the signature part of a declaration.
type DeclSignature struct {
	PP        string   `json:"pp"`
	Constants []string `json:"constants"`
	Range     Range    `json:"range"`
}

// BinderView is a single binder.
type BinderView struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	BinderInfo string `json:"binderInfo"`
}

// DeclBinders are the binders of a declaration.
type DeclBinders struct {
	PP     string       `json:"pp"`
	Groups []string     `json:"groups"`
	Map    []BinderView `json:"map"`
	Range  Range        `json:"range"`
}

// DeclType is the type part of a declaration.
type DeclType struct {
	PP        string   `json:"pp"`
	Constants []string `json:"constants"`
	Range     Range    `json:"range"`
}

// DeclValue is the value (body / proof) of a declaration.
type DeclValue struct {
	PP        string   `json:"pp"`
	Constants []string `json:"constants"`
	Range     Range    `json:"range"`
}

// OpenDecl is an `open` declaration in scope. Simple maps to a string or a
// list of strings.
type OpenDecl struct {
	Simple map[string]any    `json:"simple,omitempty"`
	Rename map[string]string `json:"rename,omitempty"`
}

// ScopeInfo describes the scope a declaration lives in.
type ScopeInfo struct {
	VarDecls      []string   `json:"varDecls"`
	IncludeVars   []string   `json:"includeVars"`
	OmitVars      []string   `json:"omitVars"`
	LevelNames    []string   `json:"levelNames"`
	CurrNamespace string     `json:"currNamespace"`
	OpenDecl      []OpenDecl `json:"openDecl"`
}

// DeclarationInfo is returned when CommandOptions.Declarations is set.
type DeclarationInfo struct {
	PP        string        `json:"pp"`
	Range     Range         `json:"range"`
	Scope     ScopeInfo     `json:"scope"`
	Name      string        `json:"name"`
	FullName  string        `json:"fullName"`
	Kind      string        `json:"kind"`
	Modifiers DeclModifiers `json:"modifiers"`
	Signature DeclSignature `json:"signature"`
	Binders   *DeclBinders  `json:"binders,omitempty"`
	Type      *DeclType     `json:"type,omitempty"`
	Value     *DeclValue    `json:"value,omitempty"`
}

// Response is implemented by [*CommandResponse] and [*ProofStepResponse].
type Response interface {
	base() *BaseResponse
	// GetErrors returns all error messages.
	GetErrors() []Message
	// GetWarnings returns all warning messages.
	GetWarnings() []Message
	// HasErrors reports whether any message is an error.
	HasErrors() bool
	// LeanCodeIsValid reports whether the code is valid; see
	// [BaseResponse.LeanCodeIsValid].
	LeanCodeIsValid(start, end *Pos, allowSorry bool) bool
}

// BaseResponse holds the fields common to all responses.
type BaseResponse struct {
	Messages []Message `json:"messages,omitempty"`
	Sorries  []Sorry   `json:"sorries,omitempty"`
}

func (r *BaseResponse) base() *BaseResponse { return r }

func (r *BaseResponse) filter(sev Severity) []Message {
	var out []Message
	for _, m := range r.Messages {
		if m.Severity == sev {
			out = append(out, m)
		}
	}
	return out
}

// GetErrors returns all error messages.
func (r *BaseResponse) GetErrors() []Message { return r.filter(SeverityError) }

// GetWarnings returns all warning messages.
func (r *BaseResponse) GetWarnings() []Message { return r.filter(SeverityWarning) }

// HasErrors reports whether any message is an error.
func (r *BaseResponse) HasErrors() bool { return len(r.GetErrors()) > 0 }

// LeanCodeIsValid reports whether the submitted code is valid Lean. Only
// messages and sorries intersecting the [start, end] line range are
// considered (nil bounds are open). When allowSorry is false, sorries and
// "declaration uses 'sorry'" warnings make the code invalid.
func (r *BaseResponse) LeanCodeIsValid(start, end *Pos, allowSorry bool) bool {
	for _, m := range r.Messages {
		m := m
		if !messageIntersectsCode(&m.StartPos, m.EndPos, start, end) {
			continue
		}
		if m.Severity == SeverityError {
			return false
		}
		if !allowSorry && (m.Data == "declaration uses 'sorry'" || m.Data == "declaration uses `sorry`") {
			return false
		}
	}
	if !allowSorry {
		for _, s := range r.Sorries {
			if messageIntersectsCode(s.StartPos, s.EndPos, start, end) {
				return false
			}
		}
	}
	return true
}

// CommandResponse answers a [CommandRequest].
type CommandResponse struct {
	// Env is the environment after running the command. With an
	// [AutoServer] and AddToSessionCache it is a negative session id.
	Env int `json:"env"`
	BaseResponse
	// Tactics is set when CommandOptions.AllTactics was requested.
	Tactics []Tactic `json:"tactics,omitempty"`
	// Declarations is set when CommandOptions.Declarations was requested.
	Declarations []DeclarationInfo `json:"declarations,omitempty"`
	// InfoTree is set when CommandOptions.InfoTree was requested.
	InfoTree []*InfoTree `json:"infotree,omitempty"`
}

// ProofStepResponse answers a [ProofStateRequest].
type ProofStepResponse struct {
	// ProofStatus is "Completed", "Incomplete" or "Error", possibly followed by
	// details such as "Incomplete: contains sorry".
	ProofStatus string `json:"proofStatus"`
	// ProofState after the step. With an [AutoServer] and AddToSessionCache it
	// is a negative session id.
	ProofState int      `json:"proofState"`
	Goals      []string `json:"goals,omitempty"`
	Traces     []string `json:"traces,omitempty"`
	BaseResponse
}

// LeanError is a REPL-level error (a response containing only "message").
// Run methods return it as their error value; use errors.As to inspect it.
type LeanError struct {
	Message string `json:"message"`
}

func (e *LeanError) Error() string { return "lean: " + e.Message }

// AsLeanError returns the *LeanError wrapped in err, if any.
func AsLeanError(err error) (*LeanError, bool) {
	var le *LeanError
	ok := errors.As(err, &le)
	return le, ok
}

// decodeResponse parses raw REPL output for a request of the given kind. A
// response whose only key is "message" (or, when emptyIsError, an empty
// object) is returned as a *LeanError.
func decodeResponse(raw []byte, kind responseKind, emptyIsError bool) (Response, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("leaninteract: could not parse the Lean server output %q: %w", raw, err)
	}
	if _, ok := keys["message"]; (ok && len(keys) == 1) || (emptyIsError && len(keys) == 0) {
		le := &LeanError{}
		if err := json.Unmarshal(raw, le); err != nil {
			return nil, err
		}
		return nil, le
	}
	var resp Response
	if kind == kindProofStep {
		resp = &ProofStepResponse{}
	} else {
		resp = &CommandResponse{}
	}
	if err := json.Unmarshal(raw, resp); err != nil {
		return nil, fmt.Errorf("leaninteract: unexpected Lean server output %q: %w", raw, err)
	}
	return resp, nil
}
