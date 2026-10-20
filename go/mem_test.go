package leaninteract

import (
	"runtime"
	"testing"
)

func TestMemoryProbes(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("memory probes only checked on linux and darwin")
	}
	if p := systemMemoryPercent(); p <= 0 || p > 100 {
		t.Fatalf("system memory percent %v", p)
	}
	srv, err := NewServer(newFakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Kill()
	if _, err := srv.RunCommand(bg, Command{Cmd: "x"}); err != nil {
		t.Fatal(err)
	}
	if mb := srv.MemoryUsage(); mb <= 0 || mb > 4096 {
		t.Fatalf("REPL memory usage %v MB", mb)
	}
	srv.Kill()
	if srv.MemoryUsage() != 0 {
		t.Fatal("killed server should report 0")
	}
}
