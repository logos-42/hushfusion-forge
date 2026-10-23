// Package proc 提供进程管理：一张进程登记表 + 取消传播 + 进程组停止 +
// 退出回收 + 并发上限。
//
// 为什么需要它（而不是各处裸 exec）：
//   - "杀了父进程留下孤儿" —— 每个子进程独立 pgid，Stop 杀整个组（-pgid）。
//   - "ctx 取消了但子进程还在跑" —— Start 派生可取消 ctx 并传播，取消即杀组。
//   - "僵尸进程" —— 每个进程配一个 waiter goroutine 负责 Wait（回收），
//     状态机 Running → Done/Failed/Killed 全部落表。
//   - "同时起了 50 个编译" —— Manager 带并发信号量，满了 fail-closed（不排队吞掉）。
//
// 纪律：进程表是进程内状态（不落盘，凭据/参数不进日志文件）；Stop 是尽力而为的
// 强杀（TERM→KILL 由调用方在沙箱层做，这里直接 KILL 组，语义最确定）。
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// State 是进程生命周期状态。
type State string

const (
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateKilled  State = "killed"
)

// ErrNotFound / ErrAlreadyRunning / ErrAtCapacity 是 fail-closed 错误。
var (
	ErrNotFound       = errors.New("proc: 找不到该进程")
	ErrAlreadyRunning = errors.New("proc: 该 id 已在运行")
	ErrAtCapacity     = errors.New("proc: 并发已满（fail-closed，不排队）")
)

// Options 是启动选项。
type Options struct {
	Dir    string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
}

// Proc 是一条登记在册的进程。
type Proc struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Args      []string  `json:"args,omitempty"`
	PID       int       `json:"pid"`
	State     State     `json:"state"`
	ExitCode  int       `json:"exit_code"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}

	killedByUs bool // Stop 显式杀过（用于把退出态判为 Killed 而非 Failed）
}

// Duration 返回运行时长（进行中则至今）。
func (p *Proc) Duration(now time.Time) time.Duration {
	end := p.EndedAt
	if end.IsZero() {
		end = now
	}
	return end.Sub(p.StartedAt)
}

// Info 是对外可见的只读快照。
type Info struct {
	ID        string
	Name      string
	PID       int
	State     State
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
}

func (p *Proc) info() Info {
	return Info{ID: p.ID, Name: p.Name, PID: p.PID, State: p.State, ExitCode: p.ExitCode, StartedAt: p.StartedAt, EndedAt: p.EndedAt}
}

// Manager 是进程登记表。
type Manager struct {
	maxConcurrent int
	now           func() time.Time

	mu     sync.Mutex
	procs  map[string]*Proc
	order  []string // 启动顺序（用于 List 稳定输出）
	sem    chan struct{}
	closed bool
}

// NewManager 造一个管理器；maxConcurrent <= 0 表示不限。
func NewManager(maxConcurrent int) *Manager {
	m := &Manager{
		maxConcurrent: maxConcurrent,
		now:           time.Now,
		procs:         map[string]*Proc{},
	}
	if maxConcurrent > 0 {
		m.sem = make(chan struct{}, maxConcurrent)
	}
	return m
}

// Start 登记并启动一个进程（独立进程组）。
func (m *Manager) Start(ctx context.Context, id, name string, args []string, opts Options) (*Proc, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("proc: id 不能为空")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("proc: 命令名不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("proc: 管理器已关闭")
	}
	if old, ok := m.procs[id]; ok && old.State == StateRunning {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, id)
	}
	m.mu.Unlock()

	// 并发闸：满了立即失败（不排队）
	if m.sem != nil {
		select {
		case m.sem <- struct{}{}:
		default:
			return nil, ErrAtCapacity
		}
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组

	p := &Proc{
		ID: id, Name: name, Args: append([]string{}, args...),
		State: StateRunning, StartedAt: m.now(),
		cmd: cmd, cancel: cancel, done: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		cancel()
		<-m.sem
		return nil, fmt.Errorf("proc: 启动 %s 失败: %w", name, err)
	}
	p.PID = cmd.Process.Pid

	m.mu.Lock()
	m.procs[id] = p
	m.order = append(m.order, id)
	m.mu.Unlock()

	go m.reap(id, p)
	return p, nil
}

// reap 等待进程结束、记录退出码、释放并发闸。
func (m *Manager) reap(id string, p *Proc) {
	defer close(p.done)
	err := p.cmd.Wait() // 由本 goroutine 回收，绝不留下僵尸
	p.EndedAt = m.now()
	p.ExitCode = exitCodeOf(err)
	switch {
	case p.killedByUs:
		p.State = StateKilled
	case err != nil:
		p.State = StateFailed
	default:
		p.State = StateDone
	}
	if m.sem != nil {
		<-m.sem
	}
}

// Stop 取消并强杀一个进程（整个进程组）。
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	p, ok := m.procs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if p.State != StateRunning {
		return nil
	}
	m.mu.Lock()
	p.killedByUs = true
	m.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.PID, syscall.SIGKILL) // 负 pgid = 整组
	}
	return nil
}

// StopAll 停止所有运行中的进程，返回停止数。
func (m *Manager) StopAll() int {
	n := 0
	for _, in := range m.List() {
		if in.State == StateRunning {
			if err := m.Stop(in.ID); err == nil {
				n++
			}
		}
	}
	return n
}

// Wait 阻塞等待某进程结束（找不到则报错）。
func (m *Manager) Wait(id string) error {
	m.mu.Lock()
	p, ok := m.procs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	<-p.done
	return nil
}

// Get 返回某进程快照。
func (m *Manager) Get(id string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return Info{}, false
	}
	return p.info(), true
}

// List 返回全部登记进程（按启动顺序）。
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.order))
	for _, id := range m.order {
		if p, ok := m.procs[id]; ok {
			out = append(out, p.info())
		}
	}
	return out
}

// Running 返回运行中进程数。
func (m *Manager) Running() int {
	n := 0
	for _, in := range m.List() {
		if in.State == StateRunning {
			n++
		}
	}
	return n
}

// Shutdown 停掉一切并标记关闭（进程内收尾）。
func (m *Manager) Shutdown() int {
	n := m.StopAll()
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return n
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
