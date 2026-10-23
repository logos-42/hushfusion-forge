// Package review 提供目标/计划/结果的独立二审（review）。
//
// 学 Hermes 的 /review（spawn 独立子代理复审），但按本仓纪律：
//
//   - fail-closed：没有"已批准的二审结论"就等于不批准。调用方（如 goal.Complete）
//     必须拿到显式 Verdict 才允许收尾。
//   - 两种 reviewer，接口同一：
//     RuleReviewer  确定性规则（摘要非空 / 有计划须有证据 / 目标须有判据），零依赖、可单测；
//     HumanReviewer  人工门（Ask 函数注入，TUI/CLI 弹提示）。
//   - 二审与被审对象解耦：review 不 import goal；反向由 review 提供一个
//     Gate(*goal.Store 用的) 适配器，依赖方向单向。
//   - 待审队列 + 结论落盘（reviews.json），进程重启后可恢复、可审计。
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kind 是被审对象类型。
type Kind string

const (
	KindGoal   Kind = "goal"
	KindPlan   Kind = "plan"
	KindResult Kind = "result"
)

// Request 是一次二审请求。
type Request struct {
	ID          string   `json:"id"`
	Kind        Kind     `json:"kind"`
	Subject     string   `json:"subject"` // 被审对象 ID（如 goal ID）
	Summary     string   `json:"summary"`
	Evidence    []string `json:"evidence,omitempty"`
	Criteria    []string `json:"criteria,omitempty"`
	RequestedAt int64    `json:"requested_at"`
}

// Verdict 是二审结论。
type Verdict struct {
	RequestID string   `json:"request_id"`
	Approved  bool     `json:"approved"`
	Reasons   []string `json:"reasons,omitempty"`
	Reviewer  string   `json:"reviewer"`
	At        int64    `json:"at"`
}

// Reviewer 是二审者。
type Reviewer interface {
	Name() string
	Review(ctx context.Context, r Request) (Verdict, error)
}

// ErrNoVerdict 表示没有二审结论（fail-closed）。
var ErrNoVerdict = errors.New("review: 没有二审结论")

// RuleReviewer 是确定性规则二审者。
type RuleReviewer struct {
	// ReviewerName 留痕用。
	ReviewerName string
	// RequireEvidence 对 plan/result 是否必须有证据（默认 true）。
	RequireEvidence bool
}

// NewRuleReviewer 造一个默认规则二审者。
func NewRuleReviewer() *RuleReviewer {
	return &RuleReviewer{ReviewerName: "rule-reviewer", RequireEvidence: true}
}

func (r *RuleReviewer) Name() string {
	if r.ReviewerName == "" {
		return "rule-reviewer"
	}
	return r.ReviewerName
}

// placeholderEvidence 是"假证据"标记：出现即视为没有真证据。
var placeholderEvidence = []string{"", "-", "—", "n/a", "无", "none", "todo", "tbd"}

func isPlaceholder(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, p := range placeholderEvidence {
		if t == p {
			return true
		}
	}
	return false
}

// Review 按规则判决（任一条不满足即拒，并给出全部理由）。
func (r *RuleReviewer) Review(_ context.Context, req Request) (Verdict, error) {
	v := Verdict{RequestID: req.ID, Reviewer: r.Name(), At: time.Now().Unix()}
	var bad []string
	if strings.TrimSpace(req.Summary) == "" {
		bad = append(bad, "摘要为空")
	}
	switch req.Kind {
	case KindGoal:
		if len(req.Criteria) == 0 {
			bad = append(bad, "目标未声明完成判据")
		}
	case KindPlan, KindResult:
		if r.RequireEvidence {
			real := 0
			for _, e := range req.Evidence {
				if !isPlaceholder(e) {
					real++
				}
			}
			if real == 0 {
				bad = append(bad, "缺少真实证据（证据为空或占位符）")
			}
		}
	case "":
		bad = append(bad, "未指明被审类型")
	default:
		bad = append(bad, "未知被审类型: "+string(req.Kind))
	}
	if strings.TrimSpace(req.Subject) == "" {
		bad = append(bad, "未指明被审对象")
	}
	v.Approved = len(bad) == 0
	v.Reasons = bad
	if v.Approved {
		v.Reasons = []string{"规则全过（摘要/类型/对象" + evidenceNote(req.Kind) + "）"}
	}
	return v, nil
}

func evidenceNote(k Kind) string {
	if k == KindPlan || k == KindResult {
		return "/证据"
	}
	return "/判据"
}

// HumanReviewer 是人工门（Ask 注入）。
type HumanReviewer struct {
	ReviewerName string
	Ask          func(r Request) (approved bool, reasons []string, err error)
}

func (h *HumanReviewer) Name() string {
	if h.ReviewerName == "" {
		return "human"
	}
	return h.ReviewerName
}

func (h *HumanReviewer) Review(_ context.Context, r Request) (Verdict, error) {
	if h.Ask == nil {
		return Verdict{}, errors.New("review: HumanReviewer 未注入 Ask")
	}
	ok, reasons, err := h.Ask(r)
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{RequestID: r.ID, Approved: ok, Reviewer: h.Name(), At: time.Now().Unix()}
	if ok {
		v.Reasons = []string{"人工批准"}
	} else {
		v.Reasons = append([]string{"人工驳回"}, reasons...)
	}
	return v, nil
}

// Queue 是二审队列（待审 + 已审，落盘）。
type Queue struct {
	path string
	mu   sync.Mutex
	data queueFile

	Now func() time.Time
}

type queueFile struct {
	Pending  []Request `json:"pending,omitempty"`
	Decided  []Verdict `json:"decided,omitempty"`
	Requests []Request `json:"requests,omitempty"` // 全量请求（供按 ID 回查）
}

// NewQueue 打开（或创建）二审队列。
func NewQueue(dir string) (*Queue, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("review: dir 不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("review: 建目录失败: %w", err)
	}
	q := &Queue{path: filepath.Join(dir, "reviews.json"), Now: time.Now}
	raw, err := os.ReadFile(q.path)
	if err != nil {
		if os.IsNotExist(err) {
			return q, nil
		}
		return nil, fmt.Errorf("review: 读 %s 失败: %w", q.path, err)
	}
	var f queueFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("review: 解析 %s 失败: %w", q.path, err)
	}
	q.data = f
	return q, nil
}

func (q *Queue) saveLocked() error {
	b, err := json.MarshalIndent(q.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
}

// Submit 提交一个二审请求（自动补 ID/时间）。
func (q *Queue) Submit(r Request) (Request, error) {
	if strings.TrimSpace(r.Subject) == "" {
		return Request{}, errors.New("review: subject 必填")
	}
	if r.Kind == "" {
		return Request{}, errors.New("review: kind 必填")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.Now().Unix()
	if strings.TrimSpace(r.ID) == "" {
		r.ID = fmt.Sprintf("rev-%d-%04d", now, len(q.data.Requests)+1)
	}
	r.RequestedAt = now
	q.data.Pending = append(q.data.Pending, r)
	q.data.Requests = append(q.data.Requests, r)
	if err := q.saveLocked(); err != nil {
		return Request{}, err
	}
	return r, nil
}

// Pending 返回待审请求。
func (q *Queue) Pending() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Request{}, q.data.Pending...)
}

// Decide 记录一个结论并把它从待审移除（fail-closed：未批准也照实记录）。
func (q *Queue) Decide(v Verdict) error {
	if strings.TrimSpace(v.RequestID) == "" {
		return errors.New("review: RequestID 必填")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	v.At = q.Now().Unix()
	kept := q.data.Pending[:0]
	found := false
	for _, r := range q.data.Pending {
		if r.ID == v.RequestID {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return fmt.Errorf("review: 找不到待审请求 %s", v.RequestID)
	}
	q.data.Pending = append([]Request{}, kept...)
	q.data.Decided = append(q.data.Decided, v)
	return q.saveLocked()
}

// LatestFor 返回某对象最新的二审结论。
func (q *Queue) LatestFor(subject string) (Verdict, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// 找该 subject 最新的请求 ID
	latestReq := ""
	var latestAt int64
	reqByID := map[string]Request{}
	for _, r := range q.data.Requests {
		reqByID[r.ID] = r
		if r.Subject == subject && r.RequestedAt >= latestAt {
			latestAt = r.RequestedAt
			latestReq = r.ID
		}
	}
	if latestReq == "" {
		return Verdict{}, false
	}
	var best Verdict
	var bestAt int64
	for _, v := range q.data.Decided {
		if v.RequestID == latestReq && v.At >= bestAt {
			bestAt = v.At
			best = v
		}
	}
	if bestAt == 0 {
		return Verdict{}, false
	}
	return best, true
}

// History 返回全部结论（新的在前）。
func (q *Queue) History() []Verdict {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := append([]Verdict{}, q.data.Decided...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// RunAndRecord 用给定 reviewer 审掉一个待审请求并记录结论。
func (q *Queue) RunAndRecord(ctx context.Context, rv Reviewer, reqID string) (Verdict, error) {
	var target Request
	found := false
	for _, r := range q.Pending() {
		if r.ID == reqID {
			target = r
			found = true
			break
		}
	}
	if !found {
		return Verdict{}, fmt.Errorf("review: 找不到待审请求 %s", reqID)
	}
	v, err := rv.Review(ctx, target)
	if err != nil {
		return Verdict{}, err
	}
	if err := q.Decide(v); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

// ApprovedGate 造一个"完成门"判定函数，调用方把它接到自己的目标状态机上
// （如 internal/agent 的任务完成前调用）：只有该 subject 有"已批准"的二审结论
// 才放行。nil 队列 / 无结论 / 未批准 ⇒ 一律不放行（fail-closed）。
func ApprovedGate(q *Queue) func(subject string) (approved bool, reason string) {
	return func(subject string) (bool, string) {
		if q == nil {
			return false, "未配置二审队列"
		}
		v, ok := q.LatestFor(subject)
		if !ok {
			return false, "无二审结论（fail-closed）"
		}
		if !v.Approved {
			return false, "二审未批准: " + strings.Join(v.Reasons, "; ")
		}
		return true, "二审批准 by " + v.Reviewer + ": " + strings.Join(v.Reasons, "; ")
	}
}
