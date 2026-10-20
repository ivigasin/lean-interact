//go:build !linux && !darwin && !windows

package leaninteract

// systemMemoryPercent is not implemented on this platform and reports 0,
// which disables system-memory based restarts.
func systemMemoryPercent() float64 { return 0 }
