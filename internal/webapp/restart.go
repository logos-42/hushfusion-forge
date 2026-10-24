package webapp

import (
	"os"
	"os/exec"
)

// startReplacement 拉起当前可执行文件的新副本。
// 子进程带 HUSHFUSION_RESTART_WAIT，先等待旧进程释放端口再监听。
func startReplacement() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "HUSHFUSION_RESTART_WAIT=1500ms")
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureDetached(cmd)
	return cmd.Start()
}
