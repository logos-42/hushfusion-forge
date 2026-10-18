// ---------------------------------------------------------------- kernel 回执

package oscore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 回执状态（os/kernel_receipts/<id>.json 的 status 字段）。
//
//	submitted —— kernel 已受理提交（channel.submit 响应 ok:true）
//	settled   —— 验收勾验通过且证据在盘（ossubmit --settle 写入）
//	failed    —— 提交被拒或 kernel 响应非 ok（fail-closed 如实记录）
const (
	StatusSubmitted = "submitted"
	StatusSettled   = "settled"
	StatusFailed    = "failed"
)

// Submission 是一次 OS→kernel 提交的 verbatim 记录。
// kernel 字段存 bridge 原始响应（不加工 —— 与「只搬运不判业务」同一纪律）。
type Submission struct {
	At        string          `json:"at"`
	Channel   string          `json:"channel"`
	TaskID    string          `json:"taskId"`
	Marker    string          `json:"marker"`
	Kernel    json.RawMessage `json:"kernel"`
	Lifecycle json.RawMessage `json:"lifecycle,omitempty"`
}

// Settle 是验收勾验记录（ossubmit --settle 写入；evidence 路径必须真实存在）。
type Settle struct {
	At         string          `json:"at"`
	Acceptance string          `json:"acceptance"`
	Checked    int             `json:"checked"`
	Total      int             `json:"total"`
	Evidence   []string        `json:"evidence"`
	Kernel     json.RawMessage `json:"kernel,omitempty"`
}

// Receipt 是一个任务的 kernel 回执（os/kernel_receipts/<task>.json）。
type Receipt struct {
	Task        string       `json:"task"`
	Status      string       `json:"status"`
	Submissions []Submission `json:"submissions"`
	Settle      *Settle      `json:"settle,omitempty"`
}

// LoadReceipt 读一份回执（损坏 = 报错，不静默 —— 回执是 K1/K2 的证据）。
func LoadReceipt(path string) (*Receipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read receipt %s: %w", path, err)
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse receipt %s: %w", path, err)
	}
	if r.Task == "" {
		return nil, fmt.Errorf("receipt %s 缺 task 字段", path)
	}
	return &r, nil
}

// SaveReceipt 落盘回执（缩进 JSON，人可读 —— 回执是给 leo 看的证据）。
func SaveReceipt(path string, r *Receipt) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir receipts: %w", err)
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// LoadReceipts 读整个回执目录，返回 {taskID: receipt}。
// 文件名必须与 task 字段一致（<task>.json）；损坏文件报错不吞。
func LoadReceipts(dir string) (map[string]*Receipt, error) {
	out := map[string]*Receipt{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // 目录不存在 = 还没有回执（K1 会对 active 任务报红）
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		r, err := LoadReceipt(path)
		if err != nil {
			return nil, err
		}
		if want := r.Task + ".json"; e.Name() != want {
			return nil, fmt.Errorf("回执文件名 %s 与 task 字段不一致（应为 %s）",
				e.Name(), want)
		}
		out[r.Task] = r
	}
	return out, nil
}
