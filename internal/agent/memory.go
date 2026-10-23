package agent

// memory.go —— 会话内记忆（KV 存储，跨回合保留；预留文件持久化槽）。
//
// 对应 Hermes 的 memory 命令面（/memory 等）：agent 可以记住跨回合的事实，
// 不必每次从对话历史里重新推断。v0.1 只做会话内 KV（进程退出即失忆 —
// 诚实边界，见设计文档 §8）；持久化（--persist）列入 v0.2。
//
// 纪律：记忆不是秘密存储 —— 敏感值不要进 memory（会随对话历史发给模型）。

import (
	"sort"
	"sync"
)

// MemStore 是会话内 KV 记忆。
type MemStore struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemStore 构造空记忆。
func NewMemStore() *MemStore {
	return &MemStore{m: map[string]string{}}
}

// Set 写一条记忆（覆盖同名）。返回旧值（可审计覆盖）。
func (ms *MemStore) Set(key, value string) (old string, existed bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	old, existed = ms.m[key]
	ms.m[key] = value
	return old, existed
}

// Get 读一条记忆。
func (ms *MemStore) Get(key string) (string, bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	v, ok := ms.m[key]
	return v, ok
}

// Delete 删除一条记忆。
func (ms *MemStore) Delete(key string) bool {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if _, ok := ms.m[key]; !ok {
		return false
	}
	delete(ms.m, key)
	return true
}

// All 返回全部记忆（键排序；审计 / 注入用）。
func (ms *MemStore) All() map[string]string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	out := make(map[string]string, len(ms.m))
	for k, v := range ms.m {
		out[k] = v
	}
	return out
}

// Snapshot 返回按键排序的 KV 列表（渲染进 system 用）。
func (ms *MemStore) Snapshot() []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	keys := make([]string, 0, len(ms.m))
	for k := range ms.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+ms.m[k])
	}
	return out
}

// Len 记忆条数。
func (ms *MemStore) Len() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return len(ms.m)
}

// Reset 清空记忆。
func (ms *MemStore) Reset() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.m = map[string]string{}
}
