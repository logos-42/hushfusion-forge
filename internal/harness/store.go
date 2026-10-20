package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// StepRecord 是 harness 落盘的最小数据单元：一次评估（一个动作一步）。
//
// 它刻意不同于 internal/registry 的 design Record —— 交换子世界的评估形状
// （参数不是 r/z/I，分数是 μ 世界的代数）与线圈设计世界不同，硬塞进后者会造出
// 第二份"差不多的"记录。这里只记实验事实，跨 run 积累成数据。
type StepRecord struct {
	RunID      string    `json:"run_id"`
	Episode    int       `json:"episode"`
	Step       int       `json:"step"`
	Seed       int       `json:"seed"`
	Action     int       `json:"action"`
	ActionKey  string    `json:"action_key"`
	Mu         float64   `json:"mu"`
	Eta        float64   `json:"eta"`
	Score      float64   `json:"score"`
	Delta      float64   `json:"delta"`
	Terminated bool      `json:"terminated"`
	Obs        []float64 `json:"obs"`
}

// Store 是只追加的 JSONL 实验 store。
type Store struct {
	path string
	mu   sync.Mutex
	n    int
}

// OpenStore 打开（必要时创建）path 处的 store，并统计已有行数。
func OpenStore(path string) (*Store, error) {
	n, err := countLines(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, n: n}, nil
}

// Len 是磁盘上的记录数。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// Append 写入一条 JSONL 记录。串行化以保证 id 连续、进程被杀只丢最后一行。
func (s *Store) Append(r StepRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("harness store: marshal: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("harness store: open %s: %w", s.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("harness store: write: %w", err)
	}
	s.n++
	return nil
}

// Records 载入全部记录（容忍末尾被截断的一行）。
func (s *Store) Records() ([]StepRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("harness store: open %s: %w", s.path, err)
	}
	defer f.Close()

	out := make([]StepRecord, 0, 256)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r StepRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			// 只容忍最后一行损坏（进程被杀的痕迹）；中间损坏是真实损伤，但
			// 这里不致命退出，只是跳过那一行。
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Episodes 把 step 记录重组为 episode（模型设计候选）：按 (run_id, episode) 分组，
// 按 step 排好动作序列，final score 取最后一步，terminated 取任一为真。
func (s *Store) Episodes() []EpisodeResult {
	recs, err := s.Records()
	if err != nil {
		return nil
	}
	type ep struct {
		EpisodeResult
		seen bool
	}
	byKey := map[string]*ep{}
	order := make([]string, 0, len(recs))
	for _, r := range recs {
		key := fmt.Sprintf("%s/%d", r.RunID, r.Episode)
		e, ok := byKey[key]
		if !ok {
			e = &ep{EpisodeResult: EpisodeResult{Episode: r.Episode, Seed: r.Seed}}
			byKey[key] = e
			order = append(order, key)
		}
		e.Seed = r.Seed
		for len(e.Actions) <= r.Step {
			e.Actions = append(e.Actions, -1)
		}
		e.Actions[r.Step] = r.Action
		e.FinalScore = r.Score // 最后一步覆盖
		if r.Terminated {
			e.Terminated = true
		}
		e.Steps = r.Step + 1
	}
	out := make([]EpisodeResult, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k].EpisodeResult)
	}
	return out
}

// countLines 统计文件行数（不存在返回 0）。
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("harness store: open %s: %w", path, err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n, nil
}
