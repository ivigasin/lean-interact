package leaninteract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRequestWireFormat(t *testing.T) {
	cases := []struct {
		req  Request
		want string
	}{
		{Command{Cmd: "def x := 1"}, `{"cmd":"def x := 1"}`},
		{Command{Cmd: "a<b", Env: Ptr(0), CommandOptions: CommandOptions{AllTactics: true, RootGoals: true, InfoTree: InfoTreeFull,
			SetOptions: Options{{Name: Name{"pp", "all"}, Value: true}}}},
			`{"cmd":"a<b","env":0,"allTactics":true,"rootGoals":true,"infotree":"full","setOptions":[[["pp","all"],true]]}`},
		{FileCommand{Path: "f.lean", CommandOptions: CommandOptions{Declarations: true}}, `{"path":"f.lean","declarations":true}`},
		{ProofStep{ProofState: 0, Tactic: "rfl"}, `{"proofState":0,"tactic":"rfl"}`},
		{PickleEnvironment{Env: 1, PickleTo: "a.olean"}, `{"env":1,"pickleTo":"a.olean"}`},
		{UnpickleEnvironment{UnpickleEnvFrom: "a.olean"}, `{"unpickleEnvFrom":"a.olean"}`},
		{PickleProofState{ProofState: 2, PickleTo: "p.olean"}, `{"proofState":2,"pickleTo":"p.olean"}`},
		{UnpickleProofState{UnpickleProofStateFrom: "p.olean", Env: Ptr(3)}, `{"unpickleProofStateFrom":"p.olean","env":3}`},
	}
	for _, c := range cases {
		got, err := marshalQuery(c.req)
		if err != nil || string(got) != c.want {
			t.Errorf("%T: got %s (%v), want %s", c.req, got, err, c.want)
		}
	}
	if err := (Command{}).validate(); err == nil {
		t.Error("empty cmd must be rejected")
	}
}

func TestAugmentRequest(t *testing.T) {
	cfg := &Config{EnableIncrementalOptimization: true, EnableParallelElaboration: true}
	orig := Command{Cmd: "x", CommandOptions: CommandOptions{SetOptions: Options{{Name: Name{"a"}, Value: 1}}}}
	got, _ := marshalQuery(augmentRequest(cfg, orig))
	want := `{"cmd":"x","incrementality":true,"setOptions":[[["a"],1],[["Elab","async"],true]]}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
	if len(orig.SetOptions) != 1 {
		t.Fatal("augmentation mutated the caller's request")
	}
	again, _ := marshalQuery(augmentRequest(cfg, augmentRequest(cfg, orig)))
	if string(again) != want {
		t.Fatalf("augmentation is not idempotent: %s", again)
	}
	if got, _ := marshalQuery(augmentRequest(cfg, ProofStep{Tactic: "rfl"})); string(got) != `{"proofState":0,"tactic":"rfl"}` {
		t.Fatalf("proof steps must not be augmented: %s", got)
	}
}

const tacticResponse = `{"sorries": [{"proofState": 0, "pos": {"line": 1, "column": 32}, "goal": "⊢ 0 = 0", "endPos": {"line": 1, "column": 37}}],
 "messages": [{"severity": "warning", "pos": {"line": 1, "column": 8}, "endPos": {"line": 1, "column": 20}, "data": "declaration uses ` + "`sorry`" + `"}],
 "env": 0}`

func TestDecodeCommandResponse(t *testing.T) {
	resp, err := decodeResponse([]byte(tacticResponse), kindCommand, false)
	if err != nil {
		t.Fatal(err)
	}
	want := &CommandResponse{
		Env: 0,
		BaseResponse: BaseResponse{
			Messages: []Message{{StartPos: Pos{1, 8}, EndPos: &Pos{1, 20}, Severity: SeverityWarning, Data: "declaration uses `sorry`"}},
			Sorries:  []Sorry{{StartPos: &Pos{1, 32}, EndPos: &Pos{1, 37}, Goal: "⊢ 0 = 0", ProofState: Ptr(0)}},
		},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("got %+v", resp)
	}
	if resp.HasErrors() || len(resp.GetWarnings()) != 1 || len(resp.GetErrors()) != 0 {
		t.Fatal("message helpers")
	}
	if !resp.LeanCodeIsValid(nil, nil, true) || resp.LeanCodeIsValid(nil, nil, false) {
		t.Fatal("LeanCodeIsValid with sorry")
	}
	// Outside the line range the sorry does not count.
	if !resp.LeanCodeIsValid(&Pos{Line: 2}, nil, false) {
		t.Fatal("LeanCodeIsValid range filtering")
	}
}

func TestDecodeProofStepAndError(t *testing.T) {
	resp, err := decodeResponse([]byte(`{"proofState": 1, "goals": [], "proofStatus": "Completed"}`), kindProofStep, false)
	if err != nil {
		t.Fatal(err)
	}
	if ps := resp.(*ProofStepResponse); ps.ProofState != 1 || ps.ProofStatus != "Completed" || len(ps.Goals) != 0 {
		t.Fatalf("got %+v", ps)
	}
	_, err = decodeResponse([]byte(`{"message": "Unknown proof state."}`), kindProofStep, false)
	le, ok := AsLeanError(err)
	if !ok || le.Message != "Unknown proof state." {
		t.Fatalf("got %v", err)
	}
	if _, err := decodeResponse([]byte(`{}`), kindCommand, true); err == nil {
		t.Fatal("{} must be a LeanError for auto servers")
	}
	if _, err := decodeResponse([]byte(`not json`), kindCommand, false); err == nil || !strings.Contains(err.Error(), "could not parse") {
		t.Fatalf("got %v", err)
	}
}

const declarationResponse = `{"env": 1,
 "messages": [{"severity": "warning", "pos": {"line": 2, "column": 14}, "endPos": {"line": 2, "column": 15}, "data": "unused"}],
 "declarations": [{"pp": "theorem test (h : p) : 0 = 0 := by rfl",
   "range": {"synthetic": false, "start": {"line": 2, "column": 0}, "finish": {"line": 2, "column": 38}},
   "scope": {"varDecls": ["variable (p : Prop)"], "includeVars": [], "omitVars": [], "levelNames": [], "currNamespace": "[anonymous]", "openDecl": []},
   "name": "test", "fullName": "test", "kind": "theorem",
   "modifiers": {"docString": null, "visibility": "regular", "computeKind": "regular", "recKind": "default", "isProtected": false, "isUnsafe": false, "attributes": []},
   "signature": {"pp": "(h : p) : 0 = 0", "constants": ["h", "p"], "range": {"synthetic": false, "start": {"line": 2, "column": 13}, "finish": {"line": 2, "column": 28}}},
   "binders": {"pp": "(h : p)", "groups": ["(h : p)"], "map": [{"id": "h", "type": "p", "binderInfo": "default"}], "range": {"synthetic": false, "start": {"line": 2, "column": 13}, "finish": {"line": 2, "column": 20}}},
   "type": {"pp": "0 = 0", "constants": [], "range": {"synthetic": false, "start": {"line": 2, "column": 23}, "finish": {"line": 2, "column": 28}}},
   "value": {"pp": ":= by rfl", "constants": [], "range": {"synthetic": false, "start": {"line": 2, "column": 29}, "finish": {"line": 2, "column": 38}}}}]}`

func TestDecodeDeclarations(t *testing.T) {
	resp, err := decodeResponse([]byte(declarationResponse), kindCommand, false)
	if err != nil {
		t.Fatal(err)
	}
	d := resp.(*CommandResponse).Declarations[0]
	if d.Kind != "theorem" || d.FullName != "test" || d.Scope.CurrNamespace != "[anonymous]" ||
		d.Binders.Map[0] != (BinderView{ID: "h", Type: "p", BinderInfo: "default"}) ||
		d.Signature.Constants[1] != "p" || d.Value.Range.Finish != (Pos{2, 38}) || d.Modifiers.DocString != nil {
		t.Fatalf("got %+v", d)
	}
	var m DeclModifiers
	if err := json.Unmarshal([]byte(`{}`), &m); err != nil || m.Visibility != "regular" || m.ComputeKind != "regular" || m.RecKind != "default" {
		t.Fatalf("defaults: %+v", m)
	}
}

func TestPosOrdering(t *testing.T) {
	a, b := Pos{1, 5}, Pos{2, 0}
	if !a.Less(b) || b.Less(a) || !a.LessEq(a) || a.Less(a) || a.Compare(b) != -1 || b.Compare(a) != 1 {
		t.Fatal("ordering")
	}
	if !(Pos{1, 1}).Less(Pos{1, 2}) {
		t.Fatal("column ordering")
	}
}
