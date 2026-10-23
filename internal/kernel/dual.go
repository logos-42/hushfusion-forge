package kernel

import (
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/kernel/ha"
)

// DualKernel 是双核冗余协调器 (对齐 bolloon 的 dual-kernel redundancy):
// 持 primary / secondary 两个 Kernel 实例, 用 ha 心跳仲裁哪个 active。所有调用路由
// 到当前 active; active 失联 (心跳缺失/过期) 时自动切到另一个。
//
// 模块化: 仲裁纯逻辑在 ha 子包, 本协调器只做「路由 + 切换」。
type DualKernel struct {
	primary   Kernel
	secondary Kernel
	version   string

	mu     sync.Mutex
	active string // ha.RolePrimary | ha.RoleSecondary
	peer   *ha.HeartbeatRecord
}

// NewDualKernel 构造双核协调器。默认 primary active。
func NewDualKernel(primary, secondary Kernel, version string) *DualKernel {
	return &DualKernel{
		primary:   primary,
		secondary: secondary,
		version:   version,
		active:    ha.RolePrimary,
	}
}

// Role 返回当前 active 角色。
func (d *DualKernel) Role() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// ObservePeer 由看门循环喂入对端心跳; 据此切换 active (纯逻辑, 可单测)。
// 仅当当前 active 是 primary 时才可能因 primary 失联切到 secondary。
func (d *DualKernel) ObservePeer(peer *ha.HeartbeatRecord, now int64) (switched bool, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.peer = peer
	if d.active == ha.RolePrimary {
		take, r := ha.DecideTakeover(peer, now, 0)
		if take {
			d.active = ha.RoleSecondary
			return true, r
		}
	}
	return false, ""
}

// Peer 返回最近一次观察到的对端心跳 (调试/观测用)。
func (d *DualKernel) Peer() *ha.HeartbeatRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peer
}

func (d *DualKernel) activeKernel() Kernel {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active == ha.RoleSecondary {
		return d.secondary
	}
	return d.primary
}

// 以下方法把 Kernel 接口全部路由到当前 active 内核。

func (d *DualKernel) ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error) {
	return d.activeKernel().ChannelSubmit(channelID, taskID, marker, sleepMs)
}
func (d *DualKernel) ChannelPeek(channelID string) (map[string]any, error) {
	return d.activeKernel().ChannelPeek(channelID)
}
func (d *DualKernel) LifecycleSubmit(request, ports map[string]any) (map[string]any, error) {
	return d.activeKernel().LifecycleSubmit(request, ports)
}
func (d *DualKernel) TransportSubmit(request, ports map[string]any) (map[string]any, error) {
	return d.activeKernel().TransportSubmit(request, ports)
}
func (d *DualKernel) ModelAcquire(snapshot, ports map[string]any) (map[string]any, error) {
	return d.activeKernel().ModelAcquire(snapshot, ports)
}
func (d *DualKernel) BolloonExec(args ...string) (map[string]any, error) {
	return d.activeKernel().BolloonExec(args...)
}

// Close 关闭两个内核实例 (幂等)。
func (d *DualKernel) Close() error {
	if d.primary != nil {
		_ = d.primary.Close()
	}
	if d.secondary != nil {
		_ = d.secondary.Close()
	}
	return nil
}
