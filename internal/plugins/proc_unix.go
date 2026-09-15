//go:build unix

package plugins

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the plugin in its own process group so stopping it stops its children.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
