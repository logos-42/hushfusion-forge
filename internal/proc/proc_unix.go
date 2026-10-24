//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// setGroup 让子进程独立进程组（Unix：Setpgid）。
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup 杀整个进程组（负 pid）。
func killGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
