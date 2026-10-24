// Package subagents 提供一个受控的子智能体集群：命名任务、并发上限、可查询、
// 可停止。它只做编排，不自己调模型 —— 执行函数由宿主（cmd/forge）注入。
package subagents

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Task 是一个子智能体任务。
type Task struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Goal     string    `json:"goal"`
	Status   string    `json:"status"` // running | done | error | stopped
	Answer   string    `json:"answer,omitempty"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Duration string    `json:"duration,omitempty"`

	cancel context.CancelFunc
}

// Manager 是并发安全的子智能体集群。
type Manager struct {
	mu      sync.Mutex
	tasks   map[string]*Task
	seq     int
	max     int
	runner  func(ctx context.Context, t *Task) (string, error)
	running int
}

// NewManager 构造集群；runner 为实际执行函数（通常由宿主提供）。
func NewManager(max int, runner func(ctx context.Context, t *Task) (string, error)) *Manager {
	if max <= 0 {
		max = 4
	}
	return &Manager{tasks: map[string]*Task{}, max: max, runner: runner}
}

// Spawn 启动一个子智能体（后台 context）；超过并发上限直接拒绝（fail-closed）。
func (m *Manager) Spawn(name, goal string) (*Task, error) {
	return m.SpawnFrom(context.Background(), name, goal)
}

// SpawnFrom 从父 context 派生（保留取消与深度标记），启动一个子智能体。
func (m *Manager) SpawnFrom(parent context.Context, name, goal string) (*Task, error) {
	if m.runner == nil {
		return nil, fmt.Errorf("subagents: 未注入执行器")
	}
	if goal == "" {
		return nil, fmt.Errorf("subagents: goal 不能为空")
	}
	m.mu.Lock()
	if m.running >= m.max {
		m.mu.Unlock()
		return nil, fmt.Errorf("subagents: 并发上限 %d，先停一个或等结果", m.max)
	}
	m.seq++
	t := &Task{
		ID:      fmt.Sprintf("sub-%d-%04d", time.Now().Unix(), m.seq),
		Name:    name,
		Goal:    goal,
		Status:  "running",
		Started: time.Now(),
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	ctx, cancel := context.WithCancel(parent)
	t.cancel = cancel
	m.tasks[t.ID] = t
	m.running++
	m.mu.Unlock()

	go func() {
		ans, err := m.runner(ctx, t)
		m.mu.Lock()
		defer m.mu.Unlock()
		t.Finished = time.Now()
		t.Duration = t.Finished.Sub(t.Started).Round(time.Millisecond).String()
		m.running--
		switch {
		case err != nil && ctx.Err() != nil:
			t.Status, t.Error = "stopped", err.Error()
		case err != nil:
			t.Status, t.Error = "error", err.Error()
		default:
			t.Status, t.Answer = "done", ans
		}
	}()
	return t, nil
}

// List 返回任务快照（按开始时间倒序）。
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		c := *t
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Result 返回单个任务。
func (m *Manager) Result(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("subagents: 找不到任务 %q", id)
	}
	return *t, nil
}

// Stop 取消一个任务。
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("subagents: 找不到任务 %q", id)
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.Status == "running" {
		t.Status = "stopped"
		t.Finished = time.Now()
	}
	return nil
}

// Running 返回运行中任务数。
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Max 返回并发上限。
func (m *Manager) Max() int { return m.max }
