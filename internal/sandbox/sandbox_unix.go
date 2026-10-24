//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }

func termGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) }
