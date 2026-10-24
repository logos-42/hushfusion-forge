//go:build windows

package proc

import (
	"errors"
	"os/exec"
)

// Windows 没有 Unix 进程组语义；这里退化为“由调用方杀本体”。
func setGroup(cmd *exec.Cmd) {}

func killGroup(int) error {
	return errors.New("windows: 进程组强杀未实现")
}
