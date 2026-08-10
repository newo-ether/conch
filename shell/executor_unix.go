//go:build !windows

package shell

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
)

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func decodeShellOutputLine(line []byte) string {
	return strings.ToValidUTF8(string(line), "\uFFFD")
}

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type unixProcessTreeController struct {
	cmd *exec.Cmd
}

func newProcessTreeController(cmd *exec.Cmd) (processTreeController, error) {
	return &unixProcessTreeController{cmd: cmd}, nil
}

func (c *unixProcessTreeController) Kill() {
	if c.cmd.Process != nil {
		// Negative PID signals the entire process group.
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (c *unixProcessTreeController) Close() {}
