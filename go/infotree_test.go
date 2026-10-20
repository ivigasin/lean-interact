package leaninteract

import (
	"encoding/json"
	"slices"
	"testing"
)

// A trimmed InfoTree for:
//
//	variable (p : Prop)
//	theorem infotree_test : 0 = 0 := by sorry
const infotreeJSON = `[
 {"kind": "CommandInfo", "node": {"elaborator": "Lean.Elab.Command.elabVariable",
   "stx": {"pp": "variable (p : Prop)", "kind": "Lean.Parser.Command.variable", "argKinds": [],
           "range": {"synthetic": false, "start": {"line": 1, "column": 0}, "finish": {"line": 1, "column": 19}}}},
  "children": []},
 {"kind": "CommandInfo", "node": {"elaborator": "Lean.Elab.Command.elabDeclaration",
   "stx": {"pp": "theorem infotree_test : 0 = 0 := by sorry\n", "kind": "Lean.Parser.Command.declaration",
           "argKinds": ["Lean.Parser.Command.declModifiers", "Lean.Parser.Command.theorem"],
           "range": {"synthetic": false, "start": {"line": 2, "column": 0}, "finish": {"line": 2, "column": 41}}}},
  "children": [
    {"kind": "TermInfo", "node": {"isBinder": false, "expr": "0 = 0", "expectedType": "Prop", "elaborator": null,
       "stx": {"pp": "0 = 0", "kind": "term", "range": {"synthetic": false, "start": {"line": 2, "column": 24}, "finish": {"line": 2, "column": 29}}}},
     "children": []},
    {"kind": "TacticInfo", "node": {"name": "Lean.Parser.Tactic.tacticSorry", "goalsBefore": ["⊢ 0 = 0"], "goalsAfter": [],
       "stx": {"pp": "sorry", "kind": "Lean.Parser.Tactic.tacticSorry", "range": {"synthetic": false, "start": {"line": 2, "column": 36}, "finish": {"line": 2, "column": 41}}}},
     "children": []}
  ]}
]`

func loadInfoTrees(t *testing.T) []*InfoTree {
	t.Helper()
	var trees []*InfoTree
	if err := json.Unmarshal([]byte(infotreeJSON), &trees); err != nil {
		t.Fatal(err)
	}
	return trees
}

func TestInfoTreeQueries(t *testing.T) {
	trees := loadInfoTrees(t)
	if n := len(slices.Collect(trees[0].Variables())); n != 1 {
		t.Fatalf("variables: %d", n)
	}
	if n := len(slices.Collect(trees[1].Theorems())); n != 1 {
		t.Fatalf("theorems: %d", n)
	}
	if n := len(slices.Collect(trees[1].Commands())); n != 1 {
		t.Fatalf("commands: %d", n)
	}
	if n := len(slices.Collect(trees[1].Docs())) + len(slices.Collect(trees[1].Namespaces())); n != 0 {
		t.Fatalf("docs/namespaces: %d", n)
	}
	var kinds []string
	for tree := range trees[1].DFSWalk() {
		kinds = append(kinds, tree.Kind)
	}
	// Python order: root, then children last-to-first.
	if !slices.Equal(kinds, []string{"CommandInfo", "TacticInfo", "TermInfo"}) {
		t.Fatalf("dfs order %v", kinds)
	}
	if n := len(slices.Collect(trees[1].Leaves())); n != 2 {
		t.Fatalf("leaves: %d", n)
	}
	tactic := slices.Collect(trees[1].Leaves())[0]
	if *tactic.Node.Name != "Lean.Parser.Tactic.tacticSorry" || tactic.Node.GoalsBefore[0] != "⊢ 0 = 0" {
		t.Fatalf("tactic node %+v", tactic.Node)
	}
}

func TestInfoTreeTheoremForSorry(t *testing.T) {
	trees := loadInfoTrees(t)
	s := Sorry{StartPos: &Pos{2, 36}, EndPos: &Pos{2, 41}, Goal: "⊢ 0 = 0"}
	if trees[1].TheoremForSorry(s) != trees[1] {
		t.Fatal("theorem not found")
	}
	if trees[1].TheoremForSorry(Sorry{StartPos: &Pos{5, 0}, EndPos: &Pos{5, 3}}) != nil {
		t.Fatal("unexpected match")
	}
}

func TestInfoTreePPUpTo(t *testing.T) {
	thm := loadInfoTrees(t)[1]
	got, err := thm.PPUpTo(Pos{2, 29})
	if err != nil || got != "theorem infotree_test : 0 = 0" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := thm.PPUpTo(Pos{3, 0}); err == nil {
		t.Fatal("out of bounds must fail")
	}
}
