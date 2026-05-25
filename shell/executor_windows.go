//go:build windows

package shell

import (
	"os/exec"
	"strconv"
)

func setSysProcAttr(cmd *exec.Cmd) {
	// No special process group setup needed; taskkill /T handles the tree.
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// taskkill /T kills the process and all descendants.
		exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
