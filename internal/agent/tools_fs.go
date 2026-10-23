package agent

// tools_fs.go —— workspace 文件与命令工具：read / ls / shell。
//
// 安全边界：
//   - read/ls 只读，路径必须落在 Config.Root 内（.. 越界一律拒绝）；
//   - shell 是写/执行工具（Write=true），必须由会话白名单显式放行
//     （Session.AllowWrites["shell"]=true），否则 ToolRegistry fail-closed 拒绝；
//   - 输出统一截断，命令有 60s 超时。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	fsReadLimit  = 32 * 1024
	shellTimeout = 60 * time.Second
)

func (s *Session) workspaceRoot() string {
	root := strings.TrimSpace(s.cfg.Root)
	if root == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// safePath 把相对路径锚定到 workspace，拒绝越界。
func (s *Session) safePath(p string) (string, error) {
	root := s.workspaceRoot()
	if strings.TrimSpace(p) == "" {
		return root, nil
	}
	abs, err := filepath.Abs(filepath.Join(root, p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越出 workspace：%s", p)
	}
	return abs, nil
}

// registerFSTools 注册 read / ls / shell 三个工具。
func registerFSTools(r *ToolRegistry, s *Session) error {
	if err := r.Register(Tool{
		Spec: mkSpec("read", "读取 workspace 内的文本文件（超过 32KB 截断）",
			map[string]string{"path": "相对 workspace 的路径"}, []string{"path"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			abs, err := s.safePath(p)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if len(b) > fsReadLimit {
				b = append(b[:fsReadLimit], []byte("\n…(截断)")...)
			}
			return string(b), nil
		},
	}); err != nil {
		return err
	}
	if err := r.Register(Tool{
		Spec: mkSpec("ls", "列出 workspace 内的目录（目录名带 /）",
			map[string]string{"path": "相对路径（空 = workspace 根）"}, nil),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			abs, err := s.safePath(p)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return "（空目录）", nil
			}
			return strings.Join(names, "\n"), nil
		},
	}); err != nil {
		return err
	}
	return r.Register(Tool{
		Spec: mkSpec("shell", "在 workspace 根目录执行 shell 命令（需白名单放行；超时 60s）",
			map[string]string{"cmd": "要执行的命令"}, []string{"cmd"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			cmdStr, _ := args["cmd"].(string)
			if strings.TrimSpace(cmdStr) == "" {
				return "", fmt.Errorf("shell: cmd 为空")
			}
			cctx, cancel := context.WithTimeout(ctx, shellTimeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, "sh", "-c", cmdStr)
			cmd.Dir = s.workspaceRoot()
			out, err := cmd.CombinedOutput()
			body := string(out)
			if len(body) > fsReadLimit {
				body = body[:fsReadLimit] + "\n…(截断)"
			}
			if err != nil {
				if body == "" {
					return "", fmt.Errorf("shell: %v", err)
				}
				return body, fmt.Errorf("shell: %v", err)
			}
			if strings.TrimSpace(body) == "" {
				return "（无输出）", nil
			}
			return body, nil
		},
	})
}
