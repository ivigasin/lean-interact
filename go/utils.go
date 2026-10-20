package leaninteract

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// String helpers for manipulating Lean source. Indices are byte offsets into
// the input string, so they can be used directly for slicing.

// IndentCode prefixes every line of code with nbSpaces spaces.
func IndentCode(code string, nbSpaces int) string {
	pad := strings.Repeat(" ", nbSpaces)
	lines := strings.Split(code, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

var (
	reBlankLine  = regexp.MustCompile(`(?m)^\s+$`)
	reMultiBreak = regexp.MustCompile(`\n\n+`)
)

// CompressNewlines empties whitespace-only lines, collapses runs of blank
// lines, strips leading whitespace and keeps at most one trailing newline.
func CompressNewlines(code string) string {
	code = reBlankLine.ReplaceAllString(code, "")
	code = reMultiBreak.ReplaceAllString(code, "\n\n")
	code = strings.TrimLeftFunc(code, unicode.IsSpace)
	if strings.HasSuffix(code, "\n") {
		code = strings.TrimRightFunc(code, unicode.IsSpace) + "\n"
	}
	return code
}

// CommentRange is a [Start, End) byte range of a comment.
type CommentRange struct{ Start, End int }

var reSingleLineComment = regexp.MustCompile(`--`)

// LeanCommentsRanges returns the merged ranges of Lean comments in code.
// multilineCommentSuffix is a regexp fragment appended to "/-" (and prepended
// to "-/"), e.g. "-" to match only docstrings. A comment opened before the
// snippet or left unclosed at its end is handled; other mismatches are an
// error. Nested comments are handled approximately, as in the Python version.
func LeanCommentsRanges(code, multilineCommentSuffix string, removeSingleLineComments bool) ([]CommentRange, error) {
	openRe, err := regexp.Compile("/-" + multilineCommentSuffix)
	if err != nil {
		return nil, err
	}
	closeRe, err := regexp.Compile(multilineCommentSuffix + "-/")
	if err != nil {
		return nil, err
	}
	var opens, closes []int
	for _, m := range openRe.FindAllStringIndex(code, -1) {
		opens = append(opens, m[0])
	}
	for _, m := range closeRe.FindAllStringIndex(code, -1) {
		closes = append(closes, m[0]+len(multilineCommentSuffix)+2)
	}
	switch {
	case len(opens) == len(closes)+1:
		// The last comment is probably not closed because the code is partial.
		closes = append(closes, len(code))
	case len(opens)+1 == len(closes):
		// The first comment was probably opened before the snippet.
		opens = append([]int{0}, opens...)
	case len(opens) != len(closes):
		return nil, errors.New("leaninteract: mismatched open and close comment indices")
	}
	ranges := make([]CommentRange, 0, len(opens))
	for i := range opens {
		ranges = append(ranges, CommentRange{opens[i], closes[i]})
	}
	if removeSingleLineComments {
		for _, m := range reSingleLineComment.FindAllStringIndex(code, -1) {
			end := strings.IndexByte(code[m[0]:], '\n')
			if end < 0 {
				// Python's str.find returns -1 here; extending to the end of
				// the string is the intended behavior.
				end = len(code)
			} else {
				end += m[0]
			}
			ranges = append(ranges, CommentRange{m[0], end})
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	var merged []CommentRange
	for _, r := range ranges {
		if n := len(merged); n > 0 && r.Start <= merged[n-1].End {
			merged[n-1].End = max(merged[n-1].End, r.End)
		} else {
			merged = append(merged, r)
		}
	}
	return merged, nil
}

// RemoveLeanComments strips all comments from code. ok is false when the
// comment structure could not be parsed.
func RemoveLeanComments(code string) (string, bool) {
	ranges, err := LeanCommentsRanges(code, "", true)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	prev := 0
	for _, r := range ranges {
		if r.Start > prev {
			b.WriteString(code[prev:r.Start])
		}
		prev = max(prev, r.End)
	}
	if prev < len(code) {
		b.WriteString(code[prev:])
	}
	return b.String(), true
}

var (
	reAssign       = regexp.MustCompile(`:=`)
	reLetAssign    = regexp.MustCompile(`let\s+\S*?\s*(:=)`)
	reHaveIAssign  = regexp.MustCompile(`haveI\s+\S*?\s*(:=)`)
	bracketClosing = map[byte]byte{')': '(', '}': '{', ']': '['}
)

// bracketScan calls stop for each byte of s[start:] with bracket depth
// zero after accounting for that byte, returning the first index where stop
// is true.
func bracketScan(s string, start int, stop func(i int, c byte) bool) (int, bool) {
	depth := map[byte]int{'(': 0, '{': 0, '[': 0}
	for i := start; i < len(s); i++ {
		c := s[i]
		if _, ok := depth[c]; ok {
			depth[c]++
		} else if open, ok := bracketClosing[c]; ok {
			depth[open]--
		}
		if depth['('] == 0 && depth['{'] == 0 && depth['['] == 0 && stop(i, c) {
			return i, true
		}
	}
	return 0, false
}

// SplitImplementation returns the index of the top-level ":=" that starts the
// implementation (the proof, for a theorem), ignoring ":=" inside brackets
// and in `let` / `haveI` bindings.
func SplitImplementation(declaration string, start int) (int, bool) {
	if !strings.Contains(declaration, ":=") {
		return 0, false
	}
	indices := map[int]bool{}
	for _, m := range reAssign.FindAllStringIndex(declaration, -1) {
		indices[m[0]] = true
	}
	for _, re := range []*regexp.Regexp{reLetAssign, reHaveIAssign} {
		for _, m := range re.FindAllStringSubmatchIndex(declaration, -1) {
			delete(indices, m[2])
		}
	}
	return bracketScan(declaration, start, func(i int, _ byte) bool { return indices[i] })
}

// SplitConclusion returns the index of the first top-level ':' in
// declaration, i.e. where the conclusion type starts.
func SplitConclusion(declaration string, start int) (int, bool) {
	return bracketScan(declaration, start, func(_ int, c byte) bool { return c == ':' })
}

var (
	// Same pattern as Python: `\btheorem|lemma|example\s`.
	reTheoremDecl  = regexp.MustCompile(`\btheorem|lemma|example\s`)
	reFirstToken   = regexp.MustCompile(`^[^\s]+`)
	reTheoremName  = regexp.MustCompile(`^[^\s:({\[]+`)
	theoremKeyword = []*regexp.Regexp{
		regexp.MustCompile(`\btheorem\s`),
		regexp.MustCompile(`\blemma\s`),
		regexp.MustCompile(`\bexample\s`),
	}
)

// CleanTheoremString removes comments and the proof from a single theorem
// statement and renames it to newTheoremName (always declared as `theorem`).
// With addSorry, " := by sorry" is appended. ok is false if the input could
// not be processed.
func CleanTheoremString(theorem, newTheoremName string, addSorry bool) (string, bool) {
	clean, ok := RemoveLeanComments(theorem)
	if !ok {
		return "", false
	}
	clean = strings.TrimSpace(clean)
	loc := reTheoremDecl.FindStringIndex(clean)
	if loc == nil {
		return "", false
	}
	clean = clean[loc[0]:]
	if idx, ok := SplitImplementation(clean, 0); ok {
		clean = strings.TrimSpace(clean[:idx])
	}
	if strings.HasPrefix(strings.TrimSpace(clean), "example") {
		clean = strings.TrimSpace(reFirstToken.ReplaceAllString(clean, ""))
	} else {
		clean = strings.TrimSpace(reFirstToken.ReplaceAllString(clean, ""))
		clean = strings.TrimSpace(reTheoremName.ReplaceAllString(clean, ""))
	}
	clean = "theorem " + newTheoremName + " " + clean
	if addSorry {
		clean += " := by sorry"
	}
	return clean, true
}

// ExtractLastTheorem returns the index where the last theorem / lemma /
// example of code starts, ignoring keywords inside comments.
//
// As in Python, candidates are collected per keyword (all theorems, then all
// lemmas, then all examples) and the last collected one wins.
func ExtractLastTheorem(code string) (int, error) {
	comments, err := LeanCommentsRanges(code, "", true)
	if err != nil {
		return 0, err
	}
	var indices []int
	for _, re := range theoremKeyword {
		for _, m := range re.FindAllStringIndex(code, -1) {
			inComment := false
			for _, c := range comments {
				if c.Start <= m[0] && m[0] <= c.End {
					inComment = true
					break
				}
			}
			if !inComment {
				indices = append(indices, m[0])
			}
		}
	}
	if len(indices) == 0 {
		return 0, fmt.Errorf("leaninteract: no theorem found in the provided Lean code:\n%s", code)
	}
	return indices[len(indices)-1], nil
}

// CleanLastTheoremString applies [CleanTheoremString] to the last theorem of
// code and keeps everything before it.
func CleanLastTheoremString(code, newTheoremName string, addSorry bool) (string, error) {
	idx, err := ExtractLastTheorem(code)
	if err != nil {
		return "", err
	}
	if thm, ok := CleanTheoremString(code[idx:], newTheoremName, addSorry); ok {
		return code[:idx] + thm, nil
	}
	return "", fmt.Errorf("leaninteract: theorem extraction failed for the following Lean code:\n%s", code)
}
