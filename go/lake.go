package leaninteract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

const leanInstallHint = "You can try to run `lean-interact install-lean` or follow: https://leanprover-community.github.io/get_started.html"

// cmdResult holds the captured output of a finished command.
type cmdResult struct {
	stdout, stderr string
}

// runCmd runs name with args in dir. When verbose is true output goes to the
// terminal, otherwise it is captured (or discarded when capture is false).
func runCmd(ctx context.Context, dir string, verbose bool, name string, args ...string) (cmdResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	if verbose {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &out, &errb
	}
	err := cmd.Run()
	return cmdResult{out.String(), errb.String()}, err
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

// CheckLake verifies that the lake executable can be run.
func CheckLake(ctx context.Context, lakePath string, verbose bool) error {
	res, err := runCmd(ctx, "", verbose, lakePath, "--version")
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("leaninteract: Lean 4 build system executable not found at `%s`: %w. %s", lakePath, err, leanInstallHint)
		}
		return fmt.Errorf("leaninteract: Lean 4 build system (`%s`) failed: %w\nstdout:\n%s\n\nstderr:\n%s", lakePath, err, res.stdout, res.stderr)
	}
	return nil
}
