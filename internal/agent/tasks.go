package agent

// tasks.go —— 目标 / 计划 / 待办存储（跨回合的长期执行）。
//
// 对应 Hermes 命令面：/goal（长期目标）、/plan（计划）、/subgoal（子目标）、
// /tasks（进行中任务）、/heartbeat（循环执行）—— runtime 提供存储与状态机，
// TUI 负责展示与交互。模型经工具读写（受 ToolRegistry 纪律），人经 TUI 覆写。
//
// 状态机：proposed → active → blocked → done | dropped（对齐 HUSHFUSION OS 的
// 任务状态机 —— 同一套纪律：状态转换必须附证据路径，AI 不能自称完成）。

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TaskState 是任务状态。
type TaskState string

const (
	TaskProposed TaskState = "proposed"
	TaskActive   TaskState = "active"
	TaskBlocked  TaskState = "blocked"
	TaskDone     TaskState = "done"
	TaskDropped  TaskState = "dropped"
)

// ValidTaskState 报告是否合法状态。
func ValidTaskState(s string) bool {
	switch TaskState(s) {
	case TaskProposed, TaskActive, TaskBlocked, TaskDone, TaskDropped:
		return true
	}
	return false
}

// Task 是一条目标/待办/计划项。
type Task struct {
	ID          string
	Title       string
	State       TaskState
	Goal        string   // 所属长期目标（空=独立任务）
	Evidence    []string // 状态转换的证据路径（铁律：转换必须附证据）
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Transitions []string // 状态转换审计（谁/何时/从哪到哪）
}

// TaskStore 是会话内任务存储。
type TaskStore struct {
	mu    sync.Mutex
	tasks map[string]*Task
	seq   int
}

// NewTaskStore 构造空任务存储。
func NewTaskStore() *TaskStore {
	return &TaskStore{tasks: map[string]*Task{}}
}

// nextID 生成形如 A-001 的自增 id（对齐 T-NN 风格；数字不补零到 2 位，因为
// 100+ 任务会溢出 —— 用 %03d 保证 1000 条内稳定）。
func (ts *TaskStore) nextID() string {
	ts.seq++
	return fmt.Sprintf("A-%03d", ts.seq)
}

// Add 新增一条任务（proposed）。返回分配的 id。
func (ts *TaskStore) Add(title, goal string) string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	id := ts.nextID()
	now := time.Now()
	ts.tasks[id] = &Task{
		ID: id, Title: title, Goal: goal, State: TaskProposed,
		CreatedAt: now, UpdatedAt: now,
		Transitions: []string{now.Format(time.RFC3339) + " created"},
	}
	return id
}

// SetState 推进状态机。非法状态转换（proposed 直接 done、AI 自称完成等）
// 返回错误 —— 调用方（工具/人）决定怎么处理。
//
// 铁律：状态转换必须附证据路径（Evidence 非空才允许 done）。
// 这与 HUSHFUSION OS 的 ossubmit --settle 同一纪律：完成只能被证明，不能被声称。
func (ts *TaskStore) SetState(id string, to TaskState, by, evidence string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tasks[id]
	if !ok {
		return fmt.Errorf("task %s 不存在", id)
	}
	if !ValidTaskState(string(to)) {
		return fmt.Errorf("非法状态 %q", to)
	}
	// 合法转换表：proposed→active|blocked|dropped；active→blocked|done|dropped；
	// blocked→active|dropped；done/dropped 终态（不可再动）。
	from := t.State
	switch from {
	case TaskDone, TaskDropped:
		return fmt.Errorf("task %s 已是终态 %s，不可转换", id, from)
	}
	if to == TaskDone && strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("task %s 转 done 必须附证据路径（完成只能被证明，不能被声称）", id)
	}
	now := time.Now()
	t.State = to
	t.UpdatedAt = now
	if strings.TrimSpace(evidence) != "" {
		t.Evidence = append(t.Evidence, evidence)
	}
	t.Transitions = append(t.Transitions, fmt.Sprintf("%s %s -> %s (by %s)", now.Format(time.RFC3339), from, to, by))
	return nil
}

// Get 读一条任务。
func (ts *TaskStore) Get(id string) (*Task, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// All 返回全部任务（按 id 排序）。
func (ts *TaskStore) All() []*Task {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ids := make([]string, 0, len(ts.tasks))
	for id := range ts.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Task, 0, len(ids))
	for _, id := range ids {
		cp := *ts.tasks[id]
		out = append(out, &cp)
	}
	return out
}

// ByGoal 返回属于某长期目标的全部任务。
func (ts *TaskStore) ByGoal(goal string) []*Task {
	var out []*Task
	for _, t := range ts.All() {
		if t.Goal == goal {
			out = append(out, t)
		}
	}
	return out
}

// Active 返回未完成任务的摘要（渲染进 system 用）。
func (ts *TaskStore) Active() []string {
	var out []string
	for _, t := range ts.All() {
		if t.State == TaskDone || t.State == TaskDropped {
			continue
		}
		line := fmt.Sprintf("[%s] %s %s — %s", t.State, t.ID, t.Goal, t.Title)
		if len(t.Evidence) > 0 {
			line += " (证据: " + strings.Join(t.Evidence, "; ") + ")"
		}
		out = append(out, line)
	}
	return out
}

// Summary 渲染任务摘要（供 system 注入；空=无活跃任务）。
func (ts *TaskStore) Summary() string {
	act := ts.Active()
	if len(act) == 0 {
		return ""
	}
	return "—— 活跃任务/目标 ——\n" + strings.Join(act, "\n")
}

// Len 任务条数。
func (ts *TaskStore) Len() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.tasks)
}

// Reset 清空任务（TUI /reset 时用）。
func (ts *TaskStore) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.tasks = map[string]*Task{}
	ts.seq = 0
}
