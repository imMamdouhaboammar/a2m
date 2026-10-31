//go:build windows

package process

import (
	"os/exec"
	"strconv"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func getProcessGroupID(pid int) int {
	return pid
}

func killProcessGroup(pid int, pgid int, force bool) error {
	flag := "/T"
	if force {
		flag = "/F"
	}
	cmd := exec.Command("taskkill", flag, "/PID", strconv.Itoa(pid))
	return cmd.Run()
}
