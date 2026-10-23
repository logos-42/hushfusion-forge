package ha

import (
	"context"
	"sync"
	"time"
)

// Guard 是双核冗余的看门循环（对齐 bolloon ha-guard）：按节拍写自身心跳、读对端
// 心跳，当对端缺失/过期时触发 onTakeover。写/读都经端口注入（filePath 或函数），
// 未注入即不动作（fail-closed，与 kernel 端口纪律一致）。
type Guard struct {
	role    string
	version string
	beatMs  int
	staleMs int
	host    string
	pid     int

	write      func(HeartbeatRecord) error      // 端口注入：写自身心跳
	readPeer   func() (*HeartbeatRecord, error) // 端口注入：读对端心跳
	onTakeover func(reason string)              // 接管回调

	mu   sync.Mutex
	rec  HeartbeatRecord
	stop chan struct{}
}

// NewGuard 构造看门器。beatMs/staleMs<=0 用默认值；write/readPeer/onTakeover
// 任一为 nil 时对应动作被跳过（fail-closed）。
func NewGuard(role, version string, beatMs, staleMs int, host string, pid int,
	write func(HeartbeatRecord) error,
	readPeer func() (*HeartbeatRecord, error),
	onTakeover func(reason string)) *Guard {
	if beatMs <= 0 {
		beatMs = BEAT_MS
	}
	if staleMs <= 0 {
		staleMs = STALE_MS
	}
	return &Guard{
		role:       role,
		version:    version,
		beatMs:     beatMs,
		staleMs:    staleMs,
		host:       host,
		pid:        pid,
		write:      write,
		readPeer:   readPeer,
		onTakeover: onTakeover,
	}
}

// Tick 执行一次心跳：写自身、读对端、必要时接管。可被测试直接调用（纯驱动）。
// 返回是否发生了接管及原因。
func (g *Guard) Tick(now int64, runIDs []string) (tookOver bool, reason string) {
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	g.mu.Lock()
	g.rec = MakeHeartbeat(g.role, g.host, g.pid, runIDs, g.version)
	g.rec.LastBeat = now
	rec := g.rec
	g.mu.Unlock()

	if g.write != nil {
		_ = g.write(rec)
	}
	if g.readPeer == nil {
		return false, ""
	}
	peer, err := g.readPeer()
	if err != nil || peer == nil {
		// 读不到对端心跳 = 对端缺失 => 接管
		if g.onTakeover != nil {
			g.onTakeover("peer-heartbeat-missing")
		}
		return true, "peer-heartbeat-missing"
	}
	take, r := DecideTakeover(peer, now, g.staleMs)
	if take && g.onTakeover != nil {
		g.onTakeover(r)
	}
	return take, r
}

// Start 启动后台看门循环（ctx 取消即停）。
func (g *Guard) Start(ctx context.Context, runIDs []string) {
	g.stop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(g.beatMs) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-g.stop:
				return
			case <-ticker.C:
				g.Tick(time.Now().UnixMilli(), runIDs)
			}
		}
	}()
}

// Stop 停止看门循环。
func (g *Guard) Stop() {
	if g.stop != nil {
		close(g.stop)
		g.stop = nil
	}
}
