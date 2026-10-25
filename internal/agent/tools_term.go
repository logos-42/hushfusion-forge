package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/privacy"
)

const (
	termMaxOutput  = 256 << 10
	termMaxTimeout = 10 * time.Minute
)

// termProc 是常驻 bash 进程（后端终端）：cwd 与 export 的变量跨调用保持。
//
// 协议：每次执行把命令写入 stdin，再追加一行 sentinel echo；从合并的
// stdout/stdout 读到 sentinel 即认为命令结束。超时杀掉进程并标记 broken，
// 下一次调用自动重启（cwd 回到 workspace 根，如实告知）。
type termProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	lines  chan termLine // 唯一 reader goroutine 产出的行流
	done   chan struct{} // 进程退出信号
	mu     sync.Mutex
	broken bool
}

type termLine struct {
	text string
	err  error
}

func startTermProc(root string) (*termProc, error) {
	bin, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("term: 找不到 bash（后端终端 fail-closed）")
	}
	cmd := exec.Command(bin, "--noprofile", "--norc")
	cmd.Dir = root
	// 不继承宿主完整 env：避免把 API key/代理凭据带进常驻终端。
	// HOME 指向 workspace 防读真实 ~/.ssh 等；npm/go 等工具的缓存重定向到
	// /tmp，防止 npm cache、Go telemetry、go-build 把杂物写进仓库目录。
	cacheRoot := filepath.Join(os.TempDir(), "forge-term-cache")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"TERM=dumb",
		"HOME=" + root,
		"PS1=",
		"npm_config_cache=" + filepath.Join(cacheRoot, "npm"),
		"XDG_CACHE_HOME=" + filepath.Join(cacheRoot, "xdg"),
		"GOCACHE=" + filepath.Join(cacheRoot, "go-build"),
		"GOTELEMETRY=off",
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	cmd.Stdout = outW
	cmd.Stderr = outW // 合并 stderr，sentinel 与报错都从同一条流读
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, err
	}
	// 写端交给子进程；父进程只保留读端。
	_ = outW.Close()
	t := &termProc{
		cmd:    cmd,
		stdin:  in,
		stdout: bufio.NewReaderSize(outR, 64*1024),
		lines:  make(chan termLine, 256),
		done:   make(chan struct{}),
	}
	// 唯一 reader：所有 exec 共享同一条行流，避免残留 goroutine 抢行。
	go func() {
		for {
			l, err := t.stdout.ReadString('\n')
			select {
			case t.lines <- termLine{text: l, err: err}:
			default: // 消费端已离开（超时等），丢行防泄漏
			}
			if err != nil {
				close(t.lines)
				return
			}
		}
	}()
	return t, nil
}

func (t *termProc) close() {
	if t == nil {
		return
	}
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	select {
	case <-t.done:
	default:
	}
	_ = t.cmd.Wait()
}

var termSentinelRe = regexp.MustCompile(`^__TERM_DONE_(\d+)_\d+__$`)

// exec 在常驻终端里执行一条命令；返回输出与退出码。
func (t *termProc) exec(cmdStr string, timeout time.Duration) (string, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken {
		return "", -1, fmt.Errorf("term: 终端已断开，请重试（会自动重启）")
	}
	nonce := time.Now().UnixNano()
	script := fmt.Sprintf("%s\nrc=$?\necho \"__TERM_DONE_${rc}_%d__\"\n", cmdStr, nonce)
	if _, err := io.WriteString(t.stdin, script); err != nil {
		t.broken = true
		return "", -1, fmt.Errorf("term: 写入命令失败: %w", err)
	}
	var b strings.Builder
	truncated := false
	rc := -1
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.broken = true
			_ = t.cmd.Process.Kill()
			if truncated {
				b.WriteString("\n…(输出截断)")
			}
			return b.String(), -1, fmt.Errorf("term: 超时（%s），终端已重启回 workspace 根", timeout)
		case l, ok := <-t.lines:
			if !ok {
				t.broken = true
				return b.String(), -1, fmt.Errorf("term: 终端进程退出")
			}
			if l.err != nil {
				t.broken = true
				return b.String(), -1, fmt.Errorf("term: 终端进程退出: %v", l.err)
			}
			if m := termSentinelRe.FindStringSubmatch(strings.TrimRight(l.text, "\r\n")); m != nil && strings.Contains(l.text, fmt.Sprintf("_%d__", nonce)) {
				fmt.Sscanf(m[1], "%d", &rc)
				if truncated {
					b.WriteString("\n…(输出截断)")
				}
				return b.String(), rc, nil
			}
			if b.Len()+len(l.text) > termMaxOutput {
				truncated = true
				continue // 继续读到 sentinel，丢弃超限输出
			}
			b.WriteString(l.text)
		}
	}
}

// registerTermTools 注册 term.run / term.reset：后端常驻终端执行。
func registerTermTools(r *ToolRegistry, s *Session) error {
	if err := r.Register(Tool{
		Spec: mkSpec("term.run", "在后端常驻终端执行命令（bash；cwd/环境跨调用保持；写动作，需白名单；输出截断 256KB）",
			map[string]string{
				"cmd":         "要执行的命令（支持 cd/export 等状态命令）",
				"timeout_sec": "超时秒数，缺省用会话 ShellTimeout，最大 600",
			}, []string{"cmd"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			cmdStr, _ := args["cmd"].(string)
			cmdStr = strings.TrimSpace(cmdStr)
			if cmdStr == "" {
				return "", fmt.Errorf("term.run: cmd 为空")
			}
			if privacy.ContainsPrivateKey(cmdStr) || privacy.SensitiveCommand(cmdStr) {
				return "", fmt.Errorf("隐私围栏拒绝：终端命令包含私钥/敏感路径")
			}
			timeout := s.cfg.ShellTimeout
			if timeout <= 0 {
				timeout = 60 * time.Second
			}
			if n, ok := args["timeout_sec"].(float64); ok && n > 0 {
				timeout = time.Duration(n) * time.Second
			}
			if timeout > termMaxTimeout {
				timeout = termMaxTimeout
			}
			if s.cfg.WorkspaceLock != nil {
				s.cfg.WorkspaceLock.Lock()
				defer s.cfg.WorkspaceLock.Unlock()
			}
			tp, err := s.termEnsure()
			if err != nil {
				return "", err
			}
			out, rc, execErr := tp.exec(cmdStr, timeout)
			if execErr != nil {
				if out == "" {
					return "", execErr
				}
				return out, execErr
			}
			if rc != 0 {
				return fmt.Sprintf("%s\n（exit code %d）", out, rc), nil
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	return r.Register(Tool{
		Spec: mkSpec("term.reset", "重置后端常驻终端（cwd/env 状态清空，回到 workspace 根）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			s.termMu.Lock()
			defer s.termMu.Unlock()
			if s.term != nil {
				s.term.close()
				s.term = nil
			}
			return "终端已重置，cwd 回到 " + s.workspaceRoot(), nil
		},
	})
}

func (s *Session) termEnsure() (*termProc, error) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if s.term != nil && !s.term.broken {
		return s.term, nil
	}
	if s.term != nil {
		s.term.close()
		s.term = nil
	}
	p, err := startTermProc(s.workspaceRoot())
	if err != nil {
		return nil, err
	}
	s.term = p
	return p, nil
}
