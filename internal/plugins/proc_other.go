//go:build !unix

package plugins

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, _ bool) { _ = cmd.Process.Kill() }
