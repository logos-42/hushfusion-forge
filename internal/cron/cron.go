// Package cron 是一个不依赖系统 crontab 的内部定时器：分钟粒度、可持久化、
// 可测试。它只负责“什么时候该跑”，不负责跑什么（调用方决定 prompt/goal）。
package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job 是一条定时任务。
type Job struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Schedule  string    `json:"scheme"`
	Prompt    string    `json:"prompt"`
	Goal      string    `json:"goal,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	LastRun   time.Time `json:"last_run,omitempty"`
	NextRun   time.Time `json:"next_run"`
}

// Store 是 JSON 持久化的任务表。
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore 打开/创建任务文件（目录自动建）。
func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
			return nil, err
		}
	}
	return &Store{path: path}, nil
}

func (s *Store) load() ([]Job, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(b, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) save(jobs []Job) error {
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List 返回全部任务（按创建时间排序）。
func (s *Store) List() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// Add 新增任务；scheme 非法直接拒绝。
func (s *Store) Add(j Job) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(j.ID) == "" {
		j.ID = fmt.Sprintf("cron-%d", time.Now().UnixNano())
	}
	next, err := Next(j.Schedule, time.Now())
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt = time.Now()
	j.NextRun = next
	if j.Prompt == "" {
		return Job{}, fmt.Errorf("cron: prompt 不能为空")
	}
	jobs = append(jobs, j)
	return j, s.save(jobs)
}

// Remove 按 ID 删除。
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	out := jobs[:0]
	found := false
	for _, j := range jobs {
		if j.ID == id {
			found = true
			continue
		}
		out = append(out, j)
	}
	if !found {
		return fmt.Errorf("cron: 找不到任务 %q", id)
	}
	return s.save(out)
}

// Due 返回到点任务（只读，不推进 next_run）。
func (s *Store) Due(now time.Time) ([]Job, error) {
	jobs, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Job
	for _, j := range jobs {
		if !j.Enabled || j.NextRun.IsZero() || j.NextRun.After(now) {
			continue // 未启用 / 没排上 / 还没到点
		}
		out = append(out, j)
	}
	return out, nil
}

// MarkRun 记录一次执行并推进下次时间。
func (s *Store) MarkRun(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID != id {
			continue
		}
		next, nerr := Next(jobs[i].Schedule, now)
		if nerr != nil {
			return nerr
		}
		jobs[i].LastRun = now
		jobs[i].NextRun = next
		return s.save(jobs)
	}
	return fmt.Errorf("cron: 找不到任务 %q", id)
}

// Next 计算 after 之后下一次触发时间（分钟粒度）。
func Next(spec string, after time.Time) (time.Time, error) {
	sch, err := Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	t := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60*5; i++ {
		if sch.match(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron: %q 5 年内没有触发点", spec)
}

// Schedule 是解析后的 5 段 cron。
type Schedule struct {
	minute, hour, dom, month, dow []int
	raw                           string
}

// Parse 支持 `分 时 日 月 周` 与 @hourly/@daily/@weekly/@monthly/@yearly。
func Parse(spec string) (Schedule, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return Schedule{}, fmt.Errorf("cron: 表达式为空")
	}
	switch strings.ToLower(s) {
	case "@hourly":
		s = "0 * * * *"
	case "@daily":
		s = "0 0 * * *"
	case "@weekly":
		s = "0 0 * * 0"
	case "@monthly":
		s = "0 0 1 * *"
	case "@yearly", "@annually":
		s = "0 0 1 1 *"
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("cron: 需要 5 段（分 时 日 月 周），got %q", spec)
	}
	minute, err := field(f[0], 0, 59)
	if err != nil {
		return Schedule{}, err
	}
	hour, err := field(f[1], 0, 23)
	if err != nil {
		return Schedule{}, err
	}
	dom, err := field(f[2], 1, 31)
	if err != nil {
		return Schedule{}, err
	}
	month, err := field(f[3], 1, 12)
	if err != nil {
		return Schedule{}, err
	}
	dow, err := field(f[4], 0, 6)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{minute: minute, hour: hour, dom: dom, month: month, dow: dow, raw: spec}, nil
}

func (s Schedule) match(t time.Time) bool {
	return contains(s.minute, t.Minute()) && contains(s.hour, t.Hour()) &&
		contains(s.dom, t.Day()) && contains(s.month, int(t.Month())) && contains(s.dow, int(t.Weekday()))
}

func field(spec string, lo, hi int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		step := 1
		if strings.Contains(part, "/") {
			bits := strings.SplitN(part, "/", 2)
			n, err := strconv.Atoi(bits[1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("cron: 步长非法 %q", part)
			}
			step = n
			part = bits[0]
		}
		var a, b int
		switch {
		case part == "*":
			a, b = lo, hi
		case strings.Contains(part, "-"):
			bits := strings.SplitN(part, "-", 2)
			var err error
			a, err = strconv.Atoi(bits[0])
			if err != nil {
				return nil, fmt.Errorf("cron: 区间非法 %q", part)
			}
			b, err = strconv.Atoi(bits[1])
			if err != nil {
				return nil, fmt.Errorf("cron: 区间非法 %q", part)
			}
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("cron: 字段非法 %q", part)
			}
			a, b = n, n
		}
		if a < lo || b > hi || a > b {
			return nil, fmt.Errorf("cron: 超出范围 %q", part)
		}
		for v := a; v <= b; v += step {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cron: 空字段 %q", spec)
	}
	return out, nil
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
