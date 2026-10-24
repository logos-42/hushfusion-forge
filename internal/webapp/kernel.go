// Package webapp 的双内核调度层。
//
// 这里不把「双内核」做成两个进程抢同一份 state（那会分裂写者），而是做成
// 进程内的两个执行 lane + 心跳 + 占用快照：
//   - 每个 lane 有独立容量，会话进入负载较低的 lane；
//   - lane 每秒写心跳到 <dir>/kernel/<role>.json，另一个 lane/运维可观察；
//   - 同一 workspace 的 shell 另有 workspace 锁，避免并发写同一目录。
//
// 这是 Linux 式的「机制分离」：模型对话是任务，lane 是 CPU，workspace 锁是
// IO 闸，room 是 IPC，memory.json 是页缓存；任何一层都不假装自己拥有全部资源。
package webapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/kernel/ha"
)

type kernelLane struct {
	role string
	max  int

	mu       sync.Mutex
	active   int
	started  int64
	lastBeat int64
}

type KernelLaneInfo struct {
	Role     string `json:"role"`
	Active   int    `json:"active"`
	Capacity int    `json:"capacity"`
	Started  int64  `json:"started_at"`
	LastBeat int64  `json:"last_beat"`
}

type KernelInfo struct {
	Active    string           `json:"active"`
	Lanes     []KernelLaneInfo `json:"lanes"`
	Runs      []KernelRunInfo  `json:"runs,omitempty"`
	HeapAlloc uint64           `json:"heap_alloc"`
	Goroutine int              `json:"goroutines"`
}

type KernelRunInfo struct {
	SessionID string `json:"session_id"`
	Lane      string `json:"lane"`
	StartedAt int64  `json:"started_at"`
	TextLen   int    `json:"text_len"`
}

func newKernelLane(role string, capacity int) *kernelLane {
	now := time.Now().UnixMilli()
	return &kernelLane{role: role, max: capacity, started: now, lastBeat: now}
}

func (l *kernelLane) tryEnter() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.max {
		return false
	}
	l.active++
	l.lastBeat = time.Now().UnixMilli()
	return true
}

func (l *kernelLane) leave() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.lastBeat = time.Now().UnixMilli()
	l.mu.Unlock()
}

func (l *kernelLane) info() KernelLaneInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return KernelLaneInfo{Role: l.role, Active: l.active, Capacity: l.max, Started: l.started, LastBeat: l.lastBeat}
}

// dualKernel 是进程内的双 lane 调度器；不是两个写者共享 state。
type dualKernel struct {
	primary   *kernelLane
	secondary *kernelLane

	mu     sync.Mutex
	active string
	base   string

	stopOnce sync.Once
	stop     chan struct{}
	started  bool
}

func newDualKernel(base string, capacity int) *dualKernel {
	if capacity <= 0 {
		capacity = 4
	}
	return &dualKernel{
		primary:   newKernelLane(ha.RolePrimary, capacity),
		secondary: newKernelLane(ha.RoleSecondary, capacity),
		active:    ha.RolePrimary,
		base:      filepath.Join(base, "kernel"),
		stop:      make(chan struct{}),
	}
}

// acquire 选当前占用更低且有容量的 lane；两者都满返回 false。
func (k *dualKernel) acquire() (*kernelLane, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	first, second := k.primary, k.secondary
	// 两个内核互相观察占用：优先选 active 更少的 lane，避免全压在一个核上。
	if second.info().Active < first.info().Active {
		first, second = second, first
	}
	if first.tryEnter() {
		k.active = first.role
		return first, true
	}
	if second.tryEnter() {
		k.active = second.role
		return second, true
	}
	return nil, false
}

func (k *dualKernel) release(lane *kernelLane) {
	if lane == nil {
		return
	}
	lane.leave()
	k.mu.Lock()
	k.active = lane.role
	k.mu.Unlock()
}

func (k *dualKernel) startHeartbeat() {
	k.mu.Lock()
	if k.started {
		k.mu.Unlock()
		return
	}
	k.started = true
	k.mu.Unlock()
	_ = os.MkdirAll(k.base, 0o700)
	k.beat()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				k.beat()
			case <-k.stop:
				return
			}
		}
	}()
}

func (k *dualKernel) close() {
	k.stopOnce.Do(func() { close(k.stop) })
}

// beat 把两个 lane 的存活/占用写成可观察心跳；读方不需要拿进程内锁。
func (k *dualKernel) beat() {
	for _, lane := range []*kernelLane{k.primary, k.secondary} {
		info := lane.info()
		rec := ha.MakeHeartbeat(info.Role, "", os.Getpid(), nil, config.ForgeVersion)
		rec.StartedAt = info.Started
		rec.LastBeat = time.Now().UnixMilli()
		b, _ := json.Marshal(rec)
		_ = os.WriteFile(filepath.Join(k.base, info.Role+".json"), b, 0o600)
	}
}

func (k *dualKernel) snapshot(extra []KernelRunInfo) KernelInfo {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	k.mu.Lock()
	active := k.active
	k.mu.Unlock()
	return KernelInfo{
		Active:    active,
		Lanes:     []KernelLaneInfo{k.primary.info(), k.secondary.info()},
		Runs:      extra,
		HeapAlloc: ms.HeapAlloc,
		Goroutine: runtime.NumGoroutine(),
	}
}
