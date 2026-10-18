// 验收文件勾验 —— settle 的前置条件：- [ ] 全部变成 - [x] 才许 done。
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var checkboxRe = regexp.MustCompile(`- \[([ xX])\]`)

// countChecklist 统计验收文件里的 checkbox：total = 全部条目，checked = 已勾条目。
func countChecklist(path string) (checked, total int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read acceptance %s: %w", path, err)
	}
	for _, m := range checkboxRe.FindAllStringSubmatch(string(raw), -1) {
		total++
		if strings.ToLower(m[1]) == "x" {
			checked++
		}
	}
	return checked, total, nil
}
