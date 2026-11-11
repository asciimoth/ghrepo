package progress

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
)

// Run starts each process on its own task and pipe, then waits before ending
// the task. Spawn also removes stale ZIG_PROGRESS values when reporting is off.
// The caller must use exec.CommandContext for process cancellation.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	_, node := Start(ctx, filepath.Base(cmd.Path), 0)
	defer node.End()
	if err := node.Spawn(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

// Output is Run with captured stdout. It preserves a configured stderr writer.
func Output(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	captureStderr := cmd.Stderr == nil
	if captureStderr {
		cmd.Stderr = &stderr
	}
	err := Run(ctx, cmd)
	var exitErr *exec.ExitError
	if captureStderr && errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// CombinedOutput is Run with stdout and stderr captured in the same buffer.
func CombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("exec: Stdout or Stderr already set")
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := Run(ctx, cmd)
	return output.Bytes(), err
}
