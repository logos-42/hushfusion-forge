// store.go — 只追加 JSONL registry 的文件管道。
//
// 放在 api.go 之外, 是为了让冻结文件只呈现被冻结的东西: 类型与被文档化的行为。
// 这里的一切都是实现选择; 它实现的语义就是 api.go 文档注释里写的那些 (外加被移除
// 的引擎中的 Python 参考实现 forge/registry.py)。
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rawLine 是 registry 文件中的一行非空的物理行。
type rawLine struct {
	no   int // 从 1 开始的物理行号, 用于人类可读的问题描述
	text string
}

// formatDesignID 返回第 n 条 record 的 design id ("D0001"), 与 Python 参考实现
// (f"D{n:04d}") 一致。record 与 lineage 边都以它为键。
func formatDesignID(n int) string { return fmt.Sprintf("D%04d", n) }

// nowUTC 是写入新 record 的时间戳。冻结文档要求 UTC (Python 参考实现写的是带偏移
// 量的本地时间; record 的时间戳只是展示用元数据, 既不属于 score, 也不属于 parity
// schema)。
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// openRegistryFile 为追加做好准备, 并返回它承载的 record 行数。文件 (及其父目录)
// 不存在时会被创建。
//
// 截断修复: 进程被杀会在文件末尾留下一个写了一半的 JSON 对象, 通常没有结尾换行。
// 这段残片会被丢弃 —— 文件通过临时文件加 rename 重写到最后一个完整行为止, 这样
// 修复中途崩溃也不会毁掉好的历史。丢弃它有两重意义: 在它之后追加会把新 record 粘
// 到残片上 (使新 record 不可读), 把它当成一行则会在 experiment_id 里留下一个永久
// 空洞, 让完整性门因为一个事后无法补救的原因变红。只有最后一个完整行之后的字节会
// 被触碰; 任何完整 record 都不会被重写, 所以 registry 在关键之处仍是只追加的。
func openRegistryFile(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("registry: empty path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("registry: create dir %s: %w", dir, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, fmt.Errorf("registry: read %s: %w", path, err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return 0, fmt.Errorf("registry: create %s: %w", path, err)
		}
		return 0, nil
	}

	lines := strings.Split(string(data), "\n")
	last := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if json.Valid([]byte(strings.TrimSpace(ln))) {
			last = i
		}
	}
	kept := lines[:last+1] // last == -1 时什么都不保留
	repaired := ""
	if last >= 0 {
		repaired = strings.Join(kept, "\n") + "\n"
	}
	if repaired != string(data) {
		if err := rewriteFile(path, []byte(repaired)); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, ln := range kept {
		if strings.TrimSpace(ln) != "" {
			n++
		}
	}
	return n, nil
}

// rewriteFile 通过临时文件 + rename 替换 path 的内容。
func rewriteFile(path string, content []byte) error {
	tmp := path + ".repair.tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return fmt.Errorf("registry: write repair tempfile %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("registry: repair %s: %w", path, err)
	}
	return nil
}

// readLines 返回每一非空行及其物理行号。整个过程持有 registry 的互斥锁, 因此
// 进程内的 Append 不会被观察到写了一半的状态; 文件缺失即视为空 registry。
func (r *Registry) readLines() ([]rawLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", r.Path, err)
	}
	var out []rawLine
	for i, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		out = append(out, rawLine{no: i + 1, text: ln})
	}
	return out, nil
}

// decodeRecord 解码一行; 当该行不是可解码的 record 时 ok 为 false (末尾被截断,
// 或类型与 schema 不符)。这样的行会被跳过, 而不是让整次读取失败, 与 Python 参考
// 实现在 json.JSONDecodeError 上的做法完全一致。
func decodeRecord(line string) (Record, bool) {
	var rec Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &rec); err != nil {
		return Record{}, false
	}
	return rec, true
}

// decodeObject 把一行解码成原始键, Check() 正是靠它看见 MISSING 的字段 (类型化
// 解码只会悄悄给出零值)。
func decodeObject(line string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return raw, nil
}

// encodeRecord 把一条 record 渲染成单行紧凑 JSON, 使用与 Python 写入方相同的设置
// (不做 HTML 转义、map 键排序、结尾换行)。Go 按声明顺序编组 struct 字段、按键序
// 编组 map, 因此对给定的 record 字节是确定的。
func encodeRecord(rec Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil { // Encode 会附加 '\n'
		return nil, fmt.Errorf("registry: encode record: %w", err)
	}
	return buf.Bytes(), nil
}

// missingRequiredKeys 报告 RequiredFields 中哪些在已编码的 record 中缺失。这是
// Python 参考实现里 append() 守卫的 Go 侧对应物
// (ValueError("registry record missing fields: ..."))。
func missingRequiredKeys(line []byte) []string {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &keys); err != nil {
		return append([]string(nil), RequiredFields...)
	}
	var missing []string
	for _, f := range RequiredFields {
		if _, ok := keys[f]; !ok {
			missing = append(missing, f)
		}
	}
	return missing
}

// appendLine 以一次写入, 把一行已带结尾的文本追加到以 O_APPEND 打开的文件, 因此
// 读者永远不会看到被撕裂的 record。
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("registry: open %s: %w", path, err)
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("registry: append %s: %w", path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("registry: close %s: %w", path, cerr)
	}
	return nil
}

// AppendAssign 是 Append 加上它分配到的 id。
//
// 为什么需要它 (对冻结接口的补充, 已上报给父线): runner.Score 必须返回一个
// EvalResult, 其 ExperimentID/DesignID 属于真正被写入的那条 record —— 搜索层是用
// DesignID 构建 lineage 树的 (见 search/api.go, “子节点以其父节点的 design_id
// 记录”)。冻结的 Append(Record) error 按值接收 record, 因此无法把分配到的 id 交还
// 回来, 而唯一的替代做法 —— 先用 NextIDs() 问计数器、再调用 Append() —— 是一个数据
// 竞争: 两个 goroutine 会拿到同一个 id。在同一把锁内分配并写入是唯一正确的实现。
// Append() 委托到这里, 所以只有一条写入路径。
func (r *Registry) AppendAssign(rec Record) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.n + 1
	if rec.ExperimentID == 0 {
		rec.ExperimentID = next
	}
	if rec.DesignID == "" {
		rec.DesignID = formatDesignID(next)
	}
	if rec.Timestamp == "" {
		rec.Timestamp = nowUTC()
	}
	line, err := encodeRecord(rec)
	if err != nil {
		return Record{}, err
	}
	if missing := missingRequiredKeys(line); len(missing) > 0 {
		return Record{}, fmt.Errorf("registry record missing fields: %v", missing)
	}
	if err := appendLine(r.Path, line); err != nil {
		return Record{}, err
	}
	r.n = next // 只在写入成功之后: id 保持无空洞
	return rec, nil
}
