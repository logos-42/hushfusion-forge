// submit / settle 核心逻辑。kernel 经接口注入 —— 单测用假 kernel，
// 集成测试用真 bridge（node + @bolloon/bolloon-kernel）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

// kernelClient 是 internal/kernelbridge.Client 的最小接口面。
type kernelClient interface {
	ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error)
	LifecycleSubmit(request, ports map[string]any) (map[string]any, error)
}

// receiptPath 是任务回执的落盘位置（os/kernel_receipts/<id>.json）。
func receiptPath(root, taskID string) string {
	return filepath.Join(root, "os", "kernel_receipts", taskID+".json")
}

// loadOrCreateReceipt 读已有回执或建新壳（保留历史 submissions —— 回执是流水）。
func loadOrCreateReceipt(root, taskID string) (*oscore.Receipt, error) {
	path := receiptPath(root, taskID)
	if r, err := oscore.LoadReceipt(path); err == nil {
		return r, nil
	}
	if _, serr := os.Stat(path); serr != nil {
		// 不存在 ⇒ 新壳；存在但损坏 ⇒ 报错（损坏回执不许静默重建）
		return &oscore.Receipt{Task: taskID}, nil
	}
	return nil, fmt.Errorf("回执损坏，拒绝覆盖: %s", path)
}

// activeTaskID 从 TASKS.md 找唯一 active 任务（0 件或 >1 件都拒绝提交）。
func activeTaskID(tasksText string) (string, error) {
	var active []string
	for _, r := range oscore.ParseTaskTable(tasksText) {
		if r.State == "active" {
			active = append(active, r.ID)
		}
	}
	switch len(active) {
	case 1:
		return active[0], nil
	case 0:
		return "", fmt.Errorf("TASKS.md 没有 active 任务（先在任务池激活一件）")
	default:
		return "", fmt.Errorf("active 任务超过 1 件（oscheck T2 会红）: %v", active)
	}
}

// submitTask 把任务经 kernel 提交（channel.submit + lifecycle 记录），
// verbatim 响应落进回执。kernel 响应非 ok ⇒ 回执 status=failed 并返回错误
// （fail-closed：拒绝假装提交成功）。
func submitTask(c kernelClient, root, taskID, tasksText string) (*oscore.Receipt, error) {
	// marker 携带任务摘要（channel.submit 的机制面只有 marker 一个字符串槽）
	marker := "os:" + taskID
	if markerMax := 200; len(marker) > markerMax {
		marker = marker[:markerMax]
	}

	kernelResp, err := c.ChannelSubmit("os", taskID, marker, 0)
	if err != nil {
		return nil, fmt.Errorf("channel.submit %s: %w", taskID, err)
	}
	kernelJSON, _ := json.Marshal(kernelResp)

	// lifecycle 记录：op=record-step 经命名端口 recordStep.echo（bridge 侧解析）
	lcResp, lcErr := c.LifecycleSubmit(
		map[string]any{"op": "record-step", "origin": "os/ossubmit",
			"runId": taskID},
		map[string]any{"recordStep": "echo"})
	var lcJSON json.RawMessage
	if lcErr == nil {
		lcJSON, _ = json.Marshal(lcResp)
	}

	receipt, err := loadOrCreateReceipt(root, taskID)
	if err != nil {
		return nil, err
	}
	receipt.Submissions = append(receipt.Submissions, oscore.Submission{
		At: time.Now().UTC().Format(time.RFC3339), Channel: "os",
		TaskID: taskID, Marker: marker, Kernel: kernelJSON, Lifecycle: lcJSON,
	})
	if ok, _ := kernelResp["ok"].(bool); ok {
		if receipt.Status != oscore.StatusSettled { // settled 不因再次提交回退
			receipt.Status = oscore.StatusSubmitted
		}
	} else {
		receipt.Status = oscore.StatusFailed
	}
	if err := oscore.SaveReceipt(receiptPath(root, taskID), receipt); err != nil {
		return nil, err
	}
	if receipt.Status == oscore.StatusFailed {
		return receipt, fmt.Errorf("kernel 拒绝了 %s（回执已如实落盘 status=failed）: %v",
			taskID, kernelResp)
	}
	return receipt, nil
}

// settleTask 验收勾验 + 证据核验 + 回执 settled + 回写 TASKS.md。
// 任一前置不满足 ⇒ 拒绝翻状态（done 不许自称）。
func settleTask(c kernelClient, root, taskID string, evidence []string) (*oscore.Receipt, error) {
	if len(evidence) == 0 {
		return nil, fmt.Errorf("--evidence 至少给一个 runs/ 路径")
	}
	for _, ev := range evidence {
		p := ev
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, ev)
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("证据路径不存在: %s", ev)
		}
	}

	receipt, err := oscore.LoadReceipt(receiptPath(root, taskID))
	if err != nil {
		return nil, fmt.Errorf("%s 还没有 kernel 回执（先 ossubmit 提交）: %w", taskID, err)
	}
	if receipt.Status == oscore.StatusFailed {
		return nil, fmt.Errorf("%s 回执是 failed（先重新 submit）", taskID)
	}
	if len(receipt.Submissions) == 0 {
		return nil, fmt.Errorf("%s 回执没有 submission 记录", taskID)
	}

	// 验收文件：os/acceptance/<prefix>*.md 恰好一份
	prefix := oscore.AcceptPrefix(taskID)
	accDir := filepath.Join(root, "os", "acceptance")
	entries, err := os.ReadDir(accDir)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(
			strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), prefix) {
			matches = append(matches, e.Name())
		}
	}
	sort.Strings(matches)
	if len(matches) != 1 {
		return nil, fmt.Errorf("os/acceptance/ 下 %s*.md 匹配到 %d 份（应恰好 1 份）: %v",
			prefix, len(matches), matches)
	}
	accPath := filepath.Join(accDir, matches[0])
	checked, total, err := countChecklist(accPath)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, fmt.Errorf("%s 没有 checkbox（验收文件格式变了？）", accPath)
	}
	if checked != total {
		return nil, fmt.Errorf("验收未过：%s 勾了 %d/%d（全勾才许 done）",
			matches[0], checked, total)
	}

	// TASKS.md 必须还是 active（done 只能从 active 翻）
	tasksPath := filepath.Join(root, "os", "TASKS.md")
	tasksText, err := os.ReadFile(tasksPath)
	if err != nil {
		return nil, err
	}
	flipped, err := flipTaskState(string(tasksText), taskID, "active", "done")
	if err != nil {
		return nil, err
	}

	// lifecycle 守成记录（同 echo 端口，verbatim 进回执）
	lcResp, lcErr := c.LifecycleSubmit(
		map[string]any{"op": "record-step", "origin": "os/ossubmit-settle",
			"runId": taskID},
		map[string]any{"recordStep": "echo"})
	var lcJSON json.RawMessage
	if lcErr == nil {
		lcJSON, _ = json.Marshal(lcResp)
	}

	receipt.Settle = &oscore.Settle{
		At:         time.Now().UTC().Format(time.RFC3339),
		Acceptance: "os/acceptance/" + matches[0],
		Checked:    checked, Total: total,
		Evidence: evidence, Kernel: lcJSON,
	}
	receipt.Status = oscore.StatusSettled
	if err := oscore.SaveReceipt(receiptPath(root, taskID), receipt); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tasksPath, []byte(flipped), 0o644); err != nil {
		return nil, fmt.Errorf("回写 TASKS.md: %w", err)
	}
	return receipt, nil
}
