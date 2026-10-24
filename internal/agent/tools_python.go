package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/privacy"
)

const (
	pythonMaxCode   = 1 << 20
	pythonMaxOutput = 256 << 10
)

// registerPythonTool 注册 python.run：在 workspace 内跑 python3。
// 用 -I 隔离模式、最小 env、workspace 锁和超时；输出仍按不可信数据回灌。
func registerPythonTool(r *ToolRegistry, s *Session) error {
	return r.Register(Tool{
		Spec: mkSpec("python.run", "在 workspace 内执行 Python（path 或 code 二选一；写动作，需白名单；输出截断 256KB）",
			map[string]string{
				"path": "workspace 内的 .py 文件（相对路径）",
				"code": "内联 Python 代码（与 path 二选一）",
			}, nil),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			path, _ := args["path"].(string)
			code = strings.TrimSpace(code)
			path = strings.TrimSpace(path)
			if code != "" && path != "" {
				return "", fmt.Errorf("python.run: path 与 code 只能选一个")
			}
			if code == "" && path == "" {
				return "", fmt.Errorf("python.run: path 或 code 必填")
			}
			if len(code) > pythonMaxCode {
				return "", fmt.Errorf("python.run: 代码超过 1MB，请用文件分块")
			}
			if privacy.ContainsPrivateKey(code) || privacy.SensitiveCommand(code) {
				return "", fmt.Errorf("隐私围栏拒绝：Python 代码包含私钥/敏感路径")
			}
			bin, err := exec.LookPath("python3")
			if err != nil {
				return "", fmt.Errorf("python.run: 找不到 python3（fail-closed）")
			}
			argv := []string{"-I"}
			if code != "" {
				argv = append(argv, "-c", code)
			} else {
				abs, err := s.safePath(path)
				if err != nil {
					return "", err
				}
				if privacy.SensitivePath(abs) {
					return "", fmt.Errorf("隐私围栏拒绝运行敏感 Python 文件")
				}
				argv = append(argv, abs)
			}
			timeout := s.cfg.ShellTimeout
			if timeout <= 0 {
				timeout = 60 * time.Second
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			if s.cfg.WorkspaceLock != nil {
				s.cfg.WorkspaceLock.Lock()
				defer s.cfg.WorkspaceLock.Unlock()
			}
			root := s.workspaceRoot()
			cmd := exec.CommandContext(cctx, bin, argv...)
			cmd.Dir = root
			// 不继承宿主完整 env（避免把 API key/代理凭据带进 Python）。
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"LANG=C.UTF-8",
				"HOME=" + root,
				"PYTHONIOENCODING=utf-8",
				"PYTHONNOUSERSITE=1",
			}
			out, runErr := cmd.CombinedOutput()
			if len(out) > pythonMaxOutput {
				out = append(out[:pythonMaxOutput], []byte("\n…(输出截断)")...)
			}
			body := string(out)
			if runErr != nil {
				if body == "" {
					return "", fmt.Errorf("python.run: %v", runErr)
				}
				return body, fmt.Errorf("python.run: %v", runErr)
			}
			return body, nil
		},
	})
}
