// Package leaninteract is a Go port of LeanInteract
// (https://github.com/augustepoiroux/LeanInteract), a library to interact with
// the Lean 4 theorem prover through the Lean REPL.
//
// The main entry points are:
//
//   - [NewConfig] prepares (clones, checks out and builds) a Lean REPL for a
//     given Lean version or [Project].
//   - [NewServer] starts a REPL process and exchanges JSON requests with it.
//   - [NewAutoServer] adds automatic restarts on memory pressure or timeouts,
//     plus a [SessionCache] that keeps selected environments and proof states
//     alive across restarts.
//   - [NewPool] runs requests on several [AutoServer] workers sharing one
//     [ReplaySessionCache].
//
// A minimal session:
//
//	cfg, err := leaninteract.NewConfig(ctx, leaninteract.ConfigOptions{LeanVersion: "v4.19.0"})
//	if err != nil { ... }
//	srv, err := leaninteract.NewServer(cfg)
//	if err != nil { ... }
//	defer srv.Kill()
//	resp, err := srv.RunCommand(ctx, leaninteract.Command{Cmd: "theorem ex (n : Nat) : n = 5 → n = 5 := id"})
package leaninteract
