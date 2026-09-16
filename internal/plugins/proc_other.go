//go:build !unix

package plugins

import "os/exec"

func setProcessGroup(*exec.Cmd) {
	// Process groups are a Unix feature; elsewhere the plugin process is killed on its own.
}

func signalGroup(cmd *exec.Cmd, _ bool) { _ = cmd.Process.Kill() }
