package leaninteract

// descendants returns root followed by all its transitive children.
func descendants(root int, children map[int][]int) []int {
	out := []int{root}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}
