package kernelbridge

// Kernel 把“一个内核实例”抽象成统一接口：背后的机制核心
// （@bolloon/bolloon-kernel 的 ChannelActor 调度 / ModelRuntime 租约 / transport /
// lifecycle / bolloon.exec）都经此暴露。双核模式下，存在 primary 与 secondary
// 两个 Kernel，由 ha 心跳仲裁谁在 active。
//
// *Client 天然满足此接口；DualKernel 自身也满足，可递归组合。
type Kernel interface {
	ChannelSubmit(args map[string]any) (map[string]any, error)
	ChannelPeek(args map[string]any) (map[string]any, error)
	LifecycleSubmit(args map[string]any) (map[string]any, error)
	TransportSubmit(args map[string]any) (map[string]any, error)
	ModelAcquire(snapshot, ports map[string]any) (map[string]any, error)
	BolloonExec(args ...string) (map[string]any, error)
	Close() error
}
