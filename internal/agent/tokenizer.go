package agent

// tokenizer.go —— 可注入的 token 计数。
//
// 为什么是"可注入"而不是"内置精确"：精确计数依赖模型的 BPE 词表（cl100k/o200k
// 都是几 MB 的二进制资源）。本仓零第三方依赖、也不往仓库塞二进制词表，所以
// **编一个"精确"就是造假**。诚实的做法是：
//
//   - 默认 LenDiv4（≈4 字节/token），Name() 里明写"估算，非精确"；
//   - 需要精确时从外部注入：HUSHFUSION_TOKENIZER_CMD 指向任意"读 stdin 文本、
//     输出一个整数"的命令（如 python + tiktoken），或自行实现 Tokenizer；
//   - 注入的实现失败时自动回退估算（ok=false），不会让整个回合失败；
//   - 即便注入了精确 tokenizer，provider 上报的真实 prompt_tokens 仍是最终依据
//     （Session.tokenScale 用它做最后校准）。

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Tokenizer 是可注入的 token 计数器。
type Tokenizer interface {
	// Count 返回一段文本的 token 数；ok=false 表示这次计数不可用（调用方应回退估算）。
	Count(text string) (n int, ok bool)
	// Name 标识实现（审计用；应能一眼看出是否精确）。
	Name() string
}

// LenDiv4 是默认估算器：约 4 字节一个 token。
// 对中文严重低估（UTF-8 一字 3 字节，实际 1~2 token），靠 tokenScale 校准兜底。
type LenDiv4 struct{}

func (LenDiv4) Count(s string) (int, bool) { return len(s) / 4, true }
func (LenDiv4) Name() string               { return "len/4（估算，非精确）" }

// CmdTokenizer 用外部命令计数：文本写 stdin，读 stdout 里的一个整数。
//
// 例：
//
//	export HUSHFUSION_TOKENIZER_CMD="python3 /path/tokcount.py"   # 内部用 tiktoken
//
// 命令必须自己决定词表（选定模型后不应再混用不同 tokenizer）。
type CmdTokenizer struct {
	// Argv 是已切分的命令（Argv[0] 为可执行名/路径）。
	Argv []string
	// Timeout 是单次计时的上限（0 = 5s）。tokenizer 慢到拖垮对话就不值得。
	Timeout time.Duration
}

func (c *CmdTokenizer) Name() string {
	if len(c.Argv) == 0 {
		return "cmd（空命令）"
	}
	return "cmd:" + strings.Join(c.Argv, " ")
}

// Count 执行外部命令计数。任何失败（命令缺失/非 0 退出/输出不是整数/超时）
// 都返回 ok=false —— 绝不猜一个数出来。
func (c *CmdTokenizer) Count(text string) (int, bool) {
	if len(c.Argv) == 0 {
		return 0, false
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// TokenizerFromEnv 按环境变量构造 tokenizer（未设 = nil，调用方用默认估算）。
//
//	HUSHFUSION_TOKENIZER_CMD="python3 /path/tokcount.py"
//
// 用空白切分（不做 shell 求值：避免把环境变量变成命令注入面）。
func TokenizerFromEnv() Tokenizer {
	raw := strings.TrimSpace(os.Getenv("HUSHFUSION_TOKENIZER_CMD"))
	if raw == "" {
		return nil
	}
	argv := strings.Fields(raw)
	if len(argv) == 0 {
		return nil
	}
	return &CmdTokenizer{Argv: argv}
}
