package main

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
	createNoWindow         = 0x08000000
)

// startDetached starts exe with args in the background with no console
// window. It breaks away from the terminal's job where Windows allows it,
// so closing the terminal does not stop the service.
func startDetached(exe string, args ...string) error {
	start := func(flags uint32) error {
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	if err := start(createNoWindow | createNewProcessGroup | createBreakawayFromJob); err == nil {
		return nil
	}
	return start(createNoWindow | createNewProcessGroup)
}
