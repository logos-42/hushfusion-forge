//go:build windows

package sandbox

import (
	"errors"
	"os/exec"
)

func setGroup(cmd *exec.Cmd) {}

func killGroup(int) error { return errors.New("windows: 进程组强杀未实现") }

func termGroup(int) error { return errors.New("windows: 进程组强杀未实现") }
