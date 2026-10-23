package agent

// memory.go —— 会话内记忆（KV 存储，跨回合保留；v0.2 起可文件持久化）。
//
// 对应 Hermes 的 memory 命令面（/memory 等）：agent 可以记住跨回合的事实，
// 不必每次从对话历史里重新推断。
//
// v0.2 持久化（--persist）：
//   - 文件后备（JSON，tmp+rename 原子替换），进程重启后记忆仍在；
//   - 文件损坏 ⇒ 构造期报错（不静默丢记忆，调用方决定怎么处理）；
//   - 文件不存在 ⇒ 空库（首次运行的正常情况）；
//   - 落盘错误不吞：记在 LastPersistErr 上，调用方/运维可读。
//
// 纪律：记忆不是秘密存储 —— 敏感值不要进 memory（会随对话历史发给模型）。
// 文件权限 0600：不是因为它装秘密，而是减少同机其他用户误读。

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// memFileVersion 是记忆文件的 schema 版本（将来迁移用）。
const memFileVersion = 1

type memFile struct {
	Version int               `json:"version"`
	Items   map[string]string `json:"items"`
}

// MemStore 是会话内 KV 记忆。
type MemStore struct {
	mu sync.Mutex
	m  map[string]string

	// path 为空 = 纯内存模式（不落盘，v0.1 行为）。
	path string

	// LastPersistErr 是最近一次落盘错误（nil = 正常）；内存模式恒 nil。
	// 不静默吞落盘失败 —— 记忆"以为记住了其实没写下去"比报错更糟。
	LastPersistErr error
}

// NewMemStore 构造空记忆（纯内存模式，不落盘）。
func NewMemStore() *MemStore {
	return &MemStore{m: map[string]string{}}
}

// NewMemStoreFile 构造文件后备记忆（path 不存在则从空库开始；损坏则报错）。
func NewMemStoreFile(path string) (*MemStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agent: 记忆文件路径不能为空")
	}
	ms := &MemStore{m: map[string]string{}, path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ms, nil // 首次运行：空库
		}
		return nil, fmt.Errorf("agent: 读记忆文件 %s 失败: %w", path, err)
	}
	var f memFile
	if err := json.Unmarshal(raw, &f); err != nil {
		// 不静默从空开始 —— 那等于悄悄丢掉全部记忆。
		return nil, fmt.Errorf("agent: 记忆文件 %s 不是合法 JSON（拒绝静默丢记忆）: %w", path, err)
	}
	for k, v := range f.Items {
		ms.m[k] = v
	}
	return ms, nil
}

// Path 返回记忆文件路径（空 = 内存模式）。
func (ms *MemStore) Path() string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.path
}

// persistLocked 原子落盘（调用方须持锁）。内存模式是 no-op。
func (ms *MemStore) persistLocked() {
	ms.LastPersistErr = nil
	if ms.path == "" {
		return
	}
	b, err := json.MarshalIndent(memFile{Version: memFileVersion, Items: ms.m}, "", "  ")
	if err != nil {
		ms.LastPersistErr = fmt.Errorf("序列化记忆失败: %w", err)
		return
	}
	tmp := ms.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		ms.LastPersistErr = fmt.Errorf("写记忆临时文件失败: %w", err)
		return
	}
	if err := os.Rename(tmp, ms.path); err != nil {
		ms.LastPersistErr = fmt.Errorf("替换记忆文件失败: %w", err)
		_ = os.Remove(tmp)
	}
}

// Set 写一条记忆（覆盖同名）。返回旧值（可审计覆盖）。
func (ms *MemStore) Set(key, value string) (old string, existed bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	old, existed = ms.m[key]
	ms.m[key] = value
	ms.persistLocked()
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
	ms.persistLocked()
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

// Reset 清空记忆（文件模式会连磁盘一起清空 —— 调用方要知道这点）。
func (ms *MemStore) Reset() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.m = map[string]string{}
	ms.persistLocked()
}

// Summary 渲染记忆摘要（供 system 注入；空=无记忆）。
func (ms *MemStore) Summary() string {
	snap := ms.Snapshot()
	if len(snap) == 0 {
		return ""
	}
	return "—— 会话记忆 ——\n" + strings.Join(snap, "\n")
}
