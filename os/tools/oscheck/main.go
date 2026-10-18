// os/tools/oscheck —— HUSHFUSION OS 决策层一致性门的命令行入口。
//
//	go run ./os/tools/oscheck          检查真实 os/ 数据（daily_check.sh 调用方式）
//	go test ./os/tools/oscheck/        变异自测（随 G5 自动进门禁）
//
// 退出码：0 = 全部通过；1 = 有违规（逐条打印）。只读不写。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

// findRoot 从当前目录向上找仓库根（以 go.mod + os/TASKS.md 同时存在为锚），
// 使二进制不依赖调用方 cwd。
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
		fmt.Println("OS CHECK: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}

	tasksPath := filepath.Join(root, "os", "TASKS.md")
	expPath := filepath.Join(root, "os", "EXPERIMENTS.md")
	acceptDir := filepath.Join(root, "os", "acceptance")

	tasksText, err := os.ReadFile(tasksPath)
	if err != nil {
		fmt.Println("OS CHECK: FAIL（缺文件 " + tasksPath + "）")
		os.Exit(1)
	}
	expText, err := os.ReadFile(expPath)
	if err != nil {
		fmt.Println("OS CHECK: FAIL（缺文件 " + expPath + "）")
		os.Exit(1)
	}
	acceptanceNames, err := readAcceptance(acceptDir)
	if err != nil {
		fmt.Println("OS CHECK: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}
	receipts, err := oscore.LoadReceipts(filepath.Join(root, "os", "kernel_receipts"))
	if err != nil {
		fmt.Println("OS CHECK: FAIL（" + err.Error() + "）")
		os.Exit(1)
	}

	if viols := runAll(string(tasksText), string(expText),
		acceptanceNames, receipts); len(viols) > 0 {
		fmt.Println("OS CHECK: FAIL")
		for _, v := range viols {
			fmt.Println("  ✗ " + v)
		}
		os.Exit(1)
	}
	fmt.Printf("OS CHECK: OK（任务池 %d 件 / 实验映射 %d 条 / acceptance %d 份 / 回执 %d 份）\n",
		len(oscore.ParseTaskTable(string(tasksText))),
		len(oscore.ParseExperimentRows(string(expText))),
		len(acceptanceNames), len(receipts))
}

// readAcceptance 列出 os/acceptance/ 下的普通文件名（目录不存在 = 空，不算错：
// T4 会在没有对应文件时如实报红）。
func readAcceptance(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 与 Python 版 sorted() 一致的确定性顺序
	return names, nil
}
