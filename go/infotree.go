package leaninteract

import (
	"errors"
	"iter"
	"strings"
)

// InfoTree kinds.
const (
	InfoKindTactic         = "TacticInfo"
	InfoKindTerm           = "TermInfo"
	InfoKindPartialTerm    = "PartialTermInfo"
	InfoKindCommand        = "CommandInfo"
	InfoKindMacroExpansion = "MacroExpansionInfo"
	InfoKindOption         = "OptionInfo"
	InfoKindField          = "FieldInfo"
	InfoKindCompletion     = "CompletionInfo"
	InfoKindUserWidget     = "UserWidgetInfo"
	InfoKindCustom         = "CustomInfo"
	InfoKindFVarAlias      = "FVarAliasInfo"
	InfoKindFieldRedecl    = "FieldRedeclInfo"
	InfoKindChoice         = "ChoiceInfo"
	InfoKindDelabTerm      = "DelabTermInfo"
)

// Syntax is a Lean syntax object.
type Syntax struct {
	// PP is the pretty-printed syntax, if available.
	PP       *string  `json:"pp"`
	Range    Range    `json:"range"`
	Kind     string   `json:"kind"`
	ArgKinds []string `json:"argKinds,omitempty"`
}

// InfoNode is a node of an [InfoTree]. The Python library distinguishes
// TacticNode, CommandNode and TermNode; here a single struct carries the union
// of their fields and the owning tree's Kind tells which ones are meaningful:
//
//   - tactic nodes: Name, GoalsBefore, GoalsAfter
//   - command nodes: Elaborator
//   - term nodes: IsBinder, Expr, ExpectedType, Elaborator
type InfoNode struct {
	Stx Syntax `json:"stx"`

	Name        *string  `json:"name,omitempty"`
	GoalsBefore []string `json:"goalsBefore,omitempty"`
	GoalsAfter  []string `json:"goalsAfter,omitempty"`

	Elaborator *string `json:"elaborator,omitempty"`

	IsBinder     bool    `json:"isBinder,omitempty"`
	Expr         string  `json:"expr,omitempty"`
	ExpectedType *string `json:"expectedType,omitempty"`
}

func (n *InfoNode) elaborator() string {
	if n == nil || n.Elaborator == nil {
		return ""
	}
	return *n.Elaborator
}

// InfoTree is the elaboration InfoTree of some Lean code.
type InfoTree struct {
	Node     *InfoNode   `json:"node"`
	Kind     string      `json:"kind"`
	Children []*InfoTree `json:"children,omitempty"`
}

// DFSWalk iterates over the tree depth-first, starting with t itself. It is
// iterative, so deep trees do not grow the stack.
//
// The visiting order matches the Python implementation: siblings are visited
// last-to-first.
func (t *InfoTree) DFSWalk() iter.Seq[*InfoTree] {
	return func(yield func(*InfoTree) bool) {
		stack := []*InfoTree{t}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(cur) {
				return
			}
			// Python's deque.extendleft(children) + popleft() pops the last
			// child first; pushing in order onto a LIFO stack does the same.
			stack = append(stack, cur.Children...)
		}
	}
}

func (t *InfoTree) filter(keep func(*InfoTree) bool) iter.Seq[*InfoTree] {
	return func(yield func(*InfoTree) bool) {
		for tree := range t.DFSWalk() {
			if keep(tree) && !yield(tree) {
				return
			}
		}
	}
}

// Leaves iterates over the trees without children.
func (t *InfoTree) Leaves() iter.Seq[*InfoTree] {
	return t.filter(func(x *InfoTree) bool { return len(x.Children) == 0 })
}

// Commands iterates over the trees representing commands.
func (t *InfoTree) Commands() iter.Seq[*InfoTree] {
	return t.filter(func(x *InfoTree) bool { return x.Kind == InfoKindCommand && x.Node != nil })
}

func (t *InfoTree) commandsWithElaborator(elab string) iter.Seq[*InfoTree] {
	return t.filter(func(x *InfoTree) bool {
		return x.Kind == InfoKindCommand && x.Node.elaborator() == elab
	})
}

// Variables iterates over `variable` commands.
func (t *InfoTree) Variables() iter.Seq[*InfoTree] {
	return t.commandsWithElaborator("Lean.Elab.Command.elabVariable")
}

// Docs iterates over module docstring commands.
func (t *InfoTree) Docs() iter.Seq[*InfoTree] {
	return t.commandsWithElaborator("Lean.Elab.Command.elabModuleDoc")
}

// Namespaces iterates over `namespace` commands.
func (t *InfoTree) Namespaces() iter.Seq[*InfoTree] {
	return t.commandsWithElaborator("Lean.Elab.Command.elabNamespace")
}

// Theorems iterates over theorem declarations (including lemmas).
func (t *InfoTree) Theorems() iter.Seq[*InfoTree] {
	return t.filter(func(x *InfoTree) bool {
		if x.Kind != InfoKindCommand || x.Node == nil {
			return false
		}
		stx := x.Node.Stx
		return stx.Kind == "Lean.Parser.Command.declaration" &&
			len(stx.ArgKinds) > 0 && stx.ArgKinds[len(stx.ArgKinds)-1] == "Lean.Parser.Command.theorem"
	})
}

// PPUpTo returns the pretty-printed source of the node up to endPos.
func (t *InfoTree) PPUpTo(endPos Pos) (string, error) {
	if t.Node == nil {
		return "", errors.New("leaninteract: InfoTree node is nil, cannot pretty-print")
	}
	r := t.Node.Stx.Range
	if r.Finish.Less(endPos) || endPos.Less(r.Start) {
		return "", errors.New("leaninteract: endPos has to be in bounds")
	}
	if t.Node.Stx.PP == nil {
		return "", errors.New("leaninteract: InfoTree node has no pretty-printed string")
	}
	lines := strings.SplitAfter(*t.Node.Stx.PP, "\n")
	var b strings.Builder
	for i := 0; i <= endPos.Line-r.Start.Line; i++ {
		if i >= len(lines) {
			return "", errors.New("leaninteract: endPos is past the pretty-printed string")
		}
		line := lines[i]
		if i == endPos.Line-r.Start.Line {
			runes := []rune(line)
			if endPos.Column < len(runes) {
				line = string(runes[:endPos.Column])
			}
		}
		b.WriteString(line)
	}
	return b.String(), nil
}

// TheoremForSorry returns the theorem tree containing the given sorry, or nil.
func (t *InfoTree) TheoremForSorry(s Sorry) *InfoTree {
	if s.StartPos == nil || s.EndPos == nil {
		return nil
	}
	for tree := range t.Theorems() {
		r := tree.Node.Stx.Range
		if s.StartPos.Less(r.Start) || r.Finish.Less(*s.EndPos) {
			continue
		}
		return tree
	}
	return nil
}
