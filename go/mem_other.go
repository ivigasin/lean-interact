//go:build !unix

package leaninteract

// processTreeRSS is not implemented on this platform and reports 0, which
// disables per-process memory based restarts.
func processTreeRSS(pid int) uint64 { return 0 }
