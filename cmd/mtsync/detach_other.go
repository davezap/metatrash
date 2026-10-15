//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// startDetached starts exe with args in its own session, so it outlives the
// terminal.
func startDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
