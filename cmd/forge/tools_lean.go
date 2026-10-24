package main

// tools_lean.go —— Lean 4 插件能力：版本检查与构建/单文件检查。
// 执行走本机 lean / lake；环境由 envload 加载并保护（不明文返回密钥）。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
)

func registerLeanTools(s *agent.Session, _ string) {
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("lean.version", "查看本机 Lean / lake 版本", nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			return runPlatformCommand(ctx, "", 30*time.Second, "lean", "--version")
		},
	})

	register(agent.Tool{
		Spec: projectTool("lean.check", "检查 Lean 代码：path 为 .lean 文件跑 lean，为目录跑 lake build（写动作，需白名单）",
			map[string]any{
				"path":        map[string]any{"type": "string", "description": "workspace 内 .lean 文件或 Lean 项目目录"},
				"timeout_sec": map[string]any{"type": "integer", "description": "超时秒数，缺省 180，最大 300"},
			}, []string{"path"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			abs, err := safeRepoPath(argString(args, "path", ""))
			if err != nil {
				return "", err
			}
			info, err := os.Stat(abs)
			if err != nil {
				return "", err
			}
			timeout := time.Duration(argInt(args, "timeout_sec", 180)) * time.Second
			if info.IsDir() {
				if _, err := exec.LookPath("lake"); err != nil {
					return "", fmt.Errorf("lean.check: 找不到 lake（Lean 工具链未装？）: %w", err)
				}
				return runPlatformCommand(ctx, "", timeout, "lake", "build")
			}
			if _, err := exec.LookPath("lean"); err != nil {
				return "", fmt.Errorf("lean.check: 找不到 lean（Lean 工具链未装？）: %w", err)
			}
			return runPlatformCommand(ctx, "", timeout, "lean", abs)
		},
	})
}
