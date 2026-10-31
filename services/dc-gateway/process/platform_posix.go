//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func getProcessGroupID(pid int) int {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		return pgid
	}
	return pid
}

func killProcessGroup(pid int, pgid int, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if pgid > 0 {
		return syscall.Kill(-pgid, sig)
	}
	return syscall.Kill(pid, sig)
}
