package leaninteract

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake Lean REPL (helper-process pattern): when
// LEAN_FAKE_REPL=1 it speaks the REPL protocol on stdin/stdout instead of
// running tests.
func TestMain(m *testing.M) {
	if os.Getenv("LEAN_FAKE_REPL") == "1" {
		fakeREPL()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeState struct {
	nextEnv, nextPS int
	envs            map[int]bool
	proofStates     map[int]bool
}

func fakeREPL() {
	st := &fakeState{envs: map[int]bool{}, proofStates: map[int]bool{}}
	in := bufio.NewReader(os.Stdin)
	var block strings.Builder
	for {
		line, err := in.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			block.WriteString(line)
		} else if block.Len() > 0 {
			var req map[string]any
			if jerr := json.Unmarshal([]byte(block.String()), &req); jerr != nil {
				fmt.Fprintf(os.Stdout, "{\"message\": %q}\n\n", "bad json: "+jerr.Error())
			} else {
				resp := st.handle(req)
				b, _ := json.Marshal(resp)
				os.Stdout.Write(append(b, '\n', '\n'))
			}
			block.Reset()
		}
		if err != nil {
			return
		}
	}
}

func intField(req map[string]any, k string) (int, bool) {
	v, ok := req[k].(float64)
	return int(v), ok
}

func (st *fakeState) newEnv() int {
	id := st.nextEnv
	st.nextEnv++
	st.envs[id] = true
	return id
}

func (st *fakeState) newPS() int {
	id := st.nextPS
	st.nextPS++
	st.proofStates[id] = true
	return id
}

func (st *fakeState) runCode(req map[string]any, code string) map[string]any {
	if env, ok := intField(req, "env"); ok && !st.envs[env] {
		return map[string]any{"message": "Unknown environment."}
	}
	switch {
	case code == "crash":
		fmt.Fprintln(os.Stderr, "fake REPL crashing")
		os.Exit(1)
	case strings.HasPrefix(code, "sleep"):
		time.Sleep(30 * time.Second)
	}
	resp := map[string]any{}
	var msgs []any
	if strings.Contains(code, "error") {
		msgs = append(msgs, map[string]any{"severity": "error", "pos": map[string]int{"line": 1, "column": 0}, "data": "unknown identifier"})
	}
	if strings.Contains(code, "sorry") {
		msgs = append(msgs, map[string]any{"severity": "warning", "pos": map[string]int{"line": 1, "column": 8},
			"endPos": map[string]int{"line": 1, "column": 20}, "data": "declaration uses `sorry`"})
		resp["sorries"] = []any{map[string]any{"proofState": st.newPS(), "goal": "⊢ 0 = 0",
			"pos": map[string]int{"line": 1, "column": 32}, "endPos": map[string]int{"line": 1, "column": 37}}}
	}
	if code == "#echo" {
		b, _ := json.Marshal(req)
		msgs = append(msgs, map[string]any{"severity": "info", "pos": map[string]int{"line": 1, "column": 0}, "data": string(b)})
	}
	if msgs != nil {
		resp["messages"] = msgs
	}
	resp["env"] = st.newEnv()
	return resp
}

func (st *fakeState) handle(req map[string]any) map[string]any {
	switch {
	case req["cmd"] != nil:
		return st.runCode(req, req["cmd"].(string))
	case req["path"] != nil:
		b, err := os.ReadFile(req["path"].(string))
		if err != nil {
			return map[string]any{"message": fmt.Sprintf("no such file or directory (error code: 2)\n  file: %s", req["path"])}
		}
		return st.runCode(req, string(b))
	case req["tactic"] != nil:
		ps, _ := intField(req, "proofState")
		if !st.proofStates[ps] {
			return map[string]any{"message": "Unknown proof state."}
		}
		if req["tactic"] == "rfl" {
			return map[string]any{"proofState": st.newPS(), "goals": []string{}, "proofStatus": "Completed"}
		}
		return map[string]any{"proofState": st.newPS(), "goals": []string{"x : Nat ⊢ x = x"}, "proofStatus": "Incomplete: open goals remain"}
	case req["pickleTo"] != nil:
		path := req["pickleTo"].(string)
		if env, ok := intField(req, "env"); ok {
			if !st.envs[env] {
				return map[string]any{"message": "Unknown environment."}
			}
			os.WriteFile(path, []byte("env"), 0o644)
			return map[string]any{"env": env}
		}
		ps, _ := intField(req, "proofState")
		if !st.proofStates[ps] {
			return map[string]any{"message": "Unknown proof state."}
		}
		os.WriteFile(path, []byte("ps"), 0o644)
		return map[string]any{"proofState": ps, "goals": []string{"⊢ 0 = 0"}, "proofStatus": "Incomplete"}
	case req["unpickleEnvFrom"] != nil:
		if _, err := os.Stat(req["unpickleEnvFrom"].(string)); err != nil {
			return map[string]any{"message": "cannot unpickle: " + err.Error()}
		}
		return map[string]any{"env": st.newEnv()}
	case req["unpickleProofStateFrom"] != nil:
		if _, err := os.Stat(req["unpickleProofStateFrom"].(string)); err != nil {
			return map[string]any{"message": "cannot unpickle: " + err.Error()}
		}
		return map[string]any{"proofState": st.newPS(), "goals": []string{"⊢ 0 = 0"}, "proofStatus": "Incomplete"}
	}
	return map[string]any{"message": "unsupported request"}
}

// fakeLakeScript emulates the lake subcommands the library calls. It logs
// its arguments to $FAKE_LAKE_LOG when set.
const fakeLakeScript = `#!/bin/sh
[ -n "$FAKE_LAKE_LOG" ] && echo "$@" >> "$FAKE_LAKE_LOG"
case "$1" in
  env) shift; exec "$@" ;;
  --version) echo "Lake version 5.0.0-fake"; exit 0 ;;
  exe) echo "error: unknown executable cache" >&2; exit 1 ;;
  +*)
    ver="${1#+}"
    if [ "$2" = "init" ]; then
      echo "import Lake" > lakefile.lean
      echo "leanprover/lean4:$ver" > lean-toolchain
    fi
    exit 0 ;;
  update) echo "{}" > lake-manifest.json; exit 0 ;;
esac
exit 0
`

// newFakeEnv writes a fake lake and a local REPL directory whose repl binary
// re-executes this test binary as the fake REPL.
func newFakeEnv(t *testing.T) (lake, replDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake REPL scripts need a POSIX shell")
	}
	dir := t.TempDir()
	lake = filepath.Join(dir, "lake")
	if err := os.WriteFile(lake, []byte(fakeLakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	replDir = filepath.Join(dir, "repl")
	bin := filepath.Join(replDir, ".lake", "build", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nLEAN_FAKE_REPL=1 exec %q -test.run=^$\n", self)
	if err := os.WriteFile(filepath.Join(bin, "repl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replDir, "lean-toolchain"), []byte("leanprover/lean4:v4.19.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return lake, replDir
}

func newFakeConfig(t *testing.T) *Config {
	t.Helper()
	lake, replDir := newFakeEnv(t)
	cfg, err := NewConfig(context.Background(), ConfigOptions{LocalREPLPath: replDir, LakePath: lake, SkipBuildREPL: true})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
