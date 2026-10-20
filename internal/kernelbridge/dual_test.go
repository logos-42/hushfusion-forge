package kernelbridge

import (
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/kernelbridge/ha"
)

// fakeKernel 是 Kernel 接口的测试替身：记录被调用的角色与次数。
type fakeKernel struct {
	role  string
	calls int
}

func (f *fakeKernel) ChannelSubmit(args map[string]any) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) ChannelPeek(args map[string]any) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) LifecycleSubmit(args map[string]any) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) TransportSubmit(args map[string]any) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) ModelAcquire(_, _ map[string]any) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) BolloonExec(args ...string) (map[string]any, error) {
	f.calls++
	return map[string]any{"via": f.role}, nil
}
func (f *fakeKernel) Close() error { return nil }

// G24k: DualKernel 默认路由到 primary，且所有 Kernel 方法都正确委托。
func TestDualKernelRoutesToPrimary(t *testing.T) {
	pri := &fakeKernel{role: ha.RolePrimary}
	sec := &fakeKernel{role: ha.RoleSecondary}
	dk := NewDualKernel(pri, sec, "0.1.0")

	if dk.Role() != ha.RolePrimary {
		t.Fatalf("默认应 primary active, got %s", dk.Role())
	}
	for _, fn := range []func(){
		func() { _, _ = dk.ChannelSubmit(nil) },
		func() { _, _ = dk.ChannelPeek(nil) },
		func() { _, _ = dk.LifecycleSubmit(nil) },
		func() { _, _ = dk.TransportSubmit(nil) },
		func() { _, _ = dk.ModelAcquire(nil, nil) },
		func() { _, _ = dk.BolloonExec("network", "status") },
	} {
		fn()
	}
	if pri.calls != 6 || sec.calls != 0 {
		t.Fatalf("primary 应被调用 6 次、secondary 0 次, got pri=%d sec=%d", pri.calls, sec.calls)
	}
}

// G24l: 当 primary 心跳缺失/过期，DualKernel 切到 secondary。
func TestDualKernelSwitchesOnStalePrimary(t *testing.T) {
	pri := &fakeKernel{role: ha.RolePrimary}
	sec := &fakeKernel{role: ha.RoleSecondary}
	dk := NewDualKernel(pri, sec, "0.1.0")

	now := int64(100000)
	stalePrimary := &ha.HeartbeatRecord{Role: ha.RolePrimary, LastBeat: now - 6000}
	switched, reason := dk.ObservePeer(stalePrimary, now)
	if !switched || reason != "primary-heartbeat-stale" {
		t.Fatalf("primary 过期应切换, got %v %q", switched, reason)
	}
	if dk.Role() != ha.RoleSecondary {
		t.Fatalf("切换后角色应为 secondary, got %s", dk.Role())
	}
	// 后续调用应路由到 secondary
	_, _ = dk.ChannelSubmit(nil)
	if sec.calls != 1 || pri.calls != 0 {
		t.Fatalf("切换后应由 secondary 处理, got pri=%d sec=%d", pri.calls, sec.calls)
	}
}

// G24m: primary 健康心跳不应触发切换。
func TestDualKernelNoSwitchOnHealthyPrimary(t *testing.T) {
	pri := &fakeKernel{role: ha.RolePrimary}
	sec := &fakeKernel{role: ha.RoleSecondary}
	dk := NewDualKernel(pri, sec, "0.1.0")

	now := int64(100000)
	healthy := &ha.HeartbeatRecord{Role: ha.RolePrimary, LastBeat: now - 1000}
	switched, _ := dk.ObservePeer(healthy, now)
	if switched {
		t.Fatal("primary 健康不应切换")
	}
	if dk.Role() != ha.RolePrimary {
		t.Fatalf("仍应 primary active, got %s", dk.Role())
	}
}
