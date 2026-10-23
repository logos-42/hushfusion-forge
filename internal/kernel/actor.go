package kernel

import (
	"sync"
	"time"
)

// ActorState 是 Channel Actor 的状态容器 (对齐原内核 createActorState 的字段)。
// 用 map 承载以便与 JSON 形状互通 (peek 直接回传, ossubmit 会整体 marshal 进回执)。
type ActorState = map[string]any

func asString(v any) string { s, _ := v.(string); return s }

func createActorState(init map[string]any) ActorState {
	if init == nil {
		init = map[string]any{}
	}
	return ActorState{
		"channelId":      asString(init["channelId"]),
		"agentId":        asString(init["agentId"]),
		"goalBinding":    asString(init["goalBinding"]),
		"messageHistory": []any{},
		"mailbox":        map[string]any{"pending": int64(0), "processed": int64(0)},
		"activeRun":      asString(init["activeRun"]),
		"cancellation":   nil,
		"outboundStream": nil,
	}
}

// ChannelActor 把一个 channel 的状态与串行执行绑在一起。
// submit 在同一 actor 上严格串行 (一把执行锁); 不同 actor 互不阻塞。
type ChannelActor struct {
	key   string
	mu    sync.Mutex
	state ActorState

	pending   int64
	processed int64
}

func newChannelActor(key string, init map[string]any) *ChannelActor {
	return &ChannelActor{key: key, state: createActorState(init)}
}

// Submit 串行执行 fn(state) 并返回其结果。执行期间占用 actor 执行锁,
// 因此同一 channel 的任务严格 FIFO、绝不并发; fn 里 sleep 多久都行。
func (a *ChannelActor) Submit(fn func(ActorState) any) any {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending++
	defer func() { a.pending--; a.processed++ }()
	a.state["mailbox"] = map[string]any{"pending": a.pending, "processed": a.processed}
	return fn(a.state)
}

// snapshot 回传一份状态浅拷贝 (peek 用)。
func (a *ChannelActor) snapshot() ActorState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(ActorState, len(a.state))
	for k, v := range a.state {
		out[k] = v
	}
	return out
}

// registry 是进程内的 actor 注册表: 一个 channel 一个 actor。
type registry struct {
	mu     sync.Mutex
	actors map[string]*ChannelActor
}

func newRegistry() *registry { return &registry{actors: map[string]*ChannelActor{}} }

// getOrCreate 取(或建)某 key 的 actor; init 只在新建时生效。
func (r *registry) getOrCreate(key string, init map[string]any) *ChannelActor {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.actors[key]; ok {
		return a
	}
	a := newChannelActor(key, init)
	r.actors[key] = a
	return a
}

// peek 只读探查 (不建)。
func (r *registry) peek(key string) *ChannelActor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actors[key]
}

// reset 清空注册表 (测试隔离用)。
func (r *registry) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actors = map[string]*ChannelActor{}
}

// sleepFor 睡眠辅助 (模拟任务耗时)。
func sleepFor(ms int) {
	if ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}
