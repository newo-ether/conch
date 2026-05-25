//go:build !windows

package shell

import (
	"os/exec"
	"syscall"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// Negative PID signals the entire process group.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
