package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
)

// Spec 是外部进程插件的装配声明。
// 兼容两种 JSON 布局：
//   - 扁平的 manifest 字段 + command/args/env/cwd/actions；
//   - {"manifest": {...}, "command": "...", ...}。
type Spec struct {
	Manifest manifest.Manifest
	Command  string
	Args     []string
	Env      []string
	Cwd      string
	Actions  []string
}

// File 是插件 JSON 文件的解析结果。
type File = Spec

// ParseFile 解析 ~/.forge/plugins/*.json。
func ParseFile(b []byte) (File, error) {
	var w struct {
		Manifest *manifest.Manifest `json:"manifest"`
		Command  string             `json:"command"`
		Args     []string           `json:"args"`
		Env      []string           `json:"env"`
		Cwd      string             `json:"cwd"`
		Actions  []string           `json:"actions"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return File{}, fmt.Errorf("plugin: manifest 解析失败: %w", err)
	}
	f := File{Command: w.Command, Args: w.Args, Env: w.Env, Cwd: w.Cwd, Actions: w.Actions}
	if w.Manifest != nil {
		f.Manifest = *w.Manifest
	} else if err := json.Unmarshal(b, &f.Manifest); err != nil {
		return File{}, fmt.Errorf("plugin: manifest 解析失败: %w", err)
	}
	if strings.TrimSpace(f.Manifest.Identity.ID) == "" && strings.TrimSpace(f.Manifest.Identity.Name) == "" {
		return File{}, errors.New("plugin: manifest 缺 identity.id 或 identity.name")
	}
	return f, nil
}

// External 是进程级插件：stdin/stdout 每行一个 JSON。
type External struct {
	spec Spec
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
}

func NewExternal(spec Spec) *External { return &External{spec: spec} }

func (e *External) Manifest() manifest.Manifest { return e.spec.Manifest }

func (e *External) Actions() []string {
	if len(e.spec.Actions) > 0 {
		return append([]string{}, e.spec.Actions...)
	}
	out := make([]string, 0, len(e.spec.Manifest.Capabilities))
	for _, c := range e.spec.Manifest.Capabilities {
		if strings.TrimSpace(c.Name) != "" {
			out = append(out, c.Name)
		}
	}
	return out
}

func (e *External) Init(_ context.Context, _ Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd != nil {
		return nil
	}
	if strings.TrimSpace(e.spec.Command) == "" {
		return errors.New("plugin: 外部插件缺 command")
	}
	cmd := exec.Command(e.spec.Command, e.spec.Args...)
	cmd.Env = append(os.Environ(), e.spec.Env...)
	cmd.Dir = e.spec.Cwd
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin: 启动 %q 失败: %w", e.spec.Command, err)
	}
	e.cmd, e.in, e.out = cmd, in, bufio.NewReader(out)
	return nil
}

func (e *External) Execute(ctx context.Context, action string, params map[string]any) (any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil || e.in == nil || e.out == nil {
		return nil, errors.New("plugin: 外部插件未 Init")
	}
	req, err := json.Marshal(map[string]any{"action": action, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := e.in.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("plugin: 写请求失败: %w", err)
	}

	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := e.out.ReadBytes('\n')
		ch <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("plugin: 读响应失败: %w", r.err)
		}
		var resp struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal(r.line, &resp); err != nil {
			return nil, fmt.Errorf("plugin: 响应不是 JSON: %w (raw=%q)", err, strings.TrimSpace(string(r.line)))
		}
		if !resp.OK {
			if resp.Error == "" {
				resp.Error = "unknown error"
			}
			return nil, errors.New(resp.Error)
		}
		var v any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &v); err != nil {
				return nil, err
			}
		}
		return v, nil
	}
}

func (e *External) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.in != nil {
		_ = e.in.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_, _ = e.cmd.Process.Wait()
	}
	e.cmd, e.in, e.out = nil, nil, nil
	return nil
}
