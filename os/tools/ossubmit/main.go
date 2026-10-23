// os/tools/ossubmit —— OS↔kernel 双向环的执行面（SPEC §0：os/ 提交任务 →
// kernel 串行执行 → 证据进 runs/ → 回执回写 → oscheck K1/K2 强制）。
//
//	go run ./os/tools/ossubmit                          提交唯一 active 任务到 kernel
//	go run ./os/tools/ossubmit --settle T-01 --evidence runs/xxx [--evidence ...]
//	                                                    验收勾验 + settled 回执 + 回写 TASKS.md
//
// 退出码：0 = 成功；1 = 拒绝（fail-closed，回执如实落盘）。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/logos-42/hushfusion-forge/internal/kernel"
)

// findRoot 从当前目录向上找仓库根（go.mod + os/TASKS.md 同时存在为锚）。
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "os", "TASKS.md")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到仓库根（go.mod + os/TASKS.md）")
		}
		dir = parent
	}
}

func main() {
	root, err := findRoot()
	if err != nil {
		fmt.Println("OSSUBMIT: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}
	// 原生内核 (internal/kernel) 进程内即用, 不再需要 node / 相对路径 bridge;
	// 仍 chdir 到仓库根, 保持工作目录一致 (回执/证据用 root 拼绝对路径)。
	if err := os.Chdir(root); err != nil {
		fmt.Println("OSSUBMIT: FAIL（chdir: " + err.Error() + "）")
		os.Exit(1)
	}

	tasksText, err := os.ReadFile(filepath.Join(root, "os", "TASKS.md"))
	if err != nil {
		fmt.Println("OSSUBMIT: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}

	c := kernel.New()
	defer c.Close()

	// --settle T-01 --evidence runs/xxx [--evidence runs/yyy]
	if len(os.Args) > 1 && os.Args[1] == "--settle" {
		runSettle(c, root, os.Args[2:])
		return
	}

	// 默认：submit 唯一 active 任务
	taskID, err := activeTaskID(string(tasksText))
	if err != nil {
		fmt.Println("OSSUBMIT: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}
	receipt, err := submitTask(c, root, taskID, string(tasksText))
	if err != nil {
		fmt.Println("OSSUBMIT: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}
	fmt.Printf("OSSUBMIT: OK（%s 经 kernel channel %q 提交，status=%s，回执 %s）\n",
		taskID, "os", receipt.Status, receiptPath(root, taskID))
}

// runSettle 解析 --settle 参数并执行验收勾验回写。
func runSettle(c *kernel.Client, root string, args []string) {
	var taskID string
	var evidence []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--settle":
			// 允许 --settle 在这里重复出现，取第一个非 flag 值
			if i+1 < len(args) && taskID == "" {
				taskID = args[i+1]
				i++
			}
		case "--evidence":
			if i+1 < len(args) {
				evidence = append(evidence, args[i+1])
				i++
			}
		default:
			if taskID == "" && args[i] != "" && args[i][0] != '-' {
				taskID = args[i]
			}
		}
	}
	if taskID == "" {
		fmt.Println("OSSUBMIT: FAIL（--settle 需要任务 ID，如 --settle T-01）")
		os.Exit(1)
	}
	receipt, err := settleTask(c, root, taskID, evidence)
	if err != nil {
		fmt.Println("OSSUBMIT: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}
	fmt.Printf("OSSUBMIT: OK（%s settled：验收 %d/%d 勾验，证据 %v，TASKS.md 已回写 done）\n",
		taskID, receipt.Settle.Checked, receipt.Settle.Total, receipt.Settle.Evidence)
}
