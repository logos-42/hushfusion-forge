package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// 默认估算器必须自报"非精确"——不能让调用方以为拿到了真 token 数。
func TestLenDiv4DeclaresItselfInexact(t *testing.T) {
	var l LenDiv4
	if !strings.Contains(l.Name(), "非精确") {
		t.Fatalf("默认估算器应自报非精确: %q", l.Name())
	}
	n, ok := l.Count("abcdefgh")
	if !ok || n != 2 {
		t.Fatalf("len/4 应给 2, got %d ok=%v", n, ok)
	}
}

// 外部命令 tokenizer：读 stdin、输出整数。
func TestCmdTokenizerSuccess(t *testing.T) {
	c := &CmdTokenizer{Argv: []string{"sh", "-c", "cat >/dev/null; echo 42"}}
	n, ok := c.Count("任意文本")
	if !ok || n != 42 {
		t.Fatalf("应读到 42, got %d ok=%v", n, ok)
	}
	if !strings.Contains(c.Name(), "sh") {
		t.Fatalf("Name 应带命令标识: %q", c.Name())
	}
}

// 命令失败/输出不是整数 ⇒ ok=false（绝不猜一个数）。
func TestCmdTokenizerFailsClosed(t *testing.T) {
	for name, c := range map[string]*CmdTokenizer{
		"非零退出": {Argv: []string{"sh", "-c", "echo 7; exit 3"}},
		"非整数":  {Argv: []string{"sh", "-c", "echo notanumber"}},
		"命令缺失": {Argv: []string{"definitely-not-a-real-binary-xyz"}},
		"空命令":  {},
	} {
		if n, ok := c.Count("x"); ok {
			t.Fatalf("%s: 应 fail-closed, got n=%d ok=true", name, n)
		}
	}
}

func TestTokenizerFromEnv(t *testing.T) {
	t.Setenv("HUSHFUSION_TOKENIZER_CMD", "")
	if got := TokenizerFromEnv(); got != nil {
		t.Fatalf("未设 env 应返回 nil, got %v", got)
	}
	t.Setenv("HUSHFUSION_TOKENIZER_CMD", "sh -c 'echo 5'")
	got := TokenizerFromEnv()
	if got == nil || !strings.Contains(got.Name(), "cmd:") {
		t.Fatalf("应构造 CmdTokenizer, got %v", got)
	}
}

// 注入精确 tokenizer 后，预算判断按它给的数字走（这里用"每字节 1 token"的夸张实现）。
type byteTokenizer struct{}

func (byteTokenizer) Count(s string) (int, bool) { return len(s), true }
func (byteTokenizer) Name() string               { return "test:bytes" }

func TestInjectedTokenizerDrivesCompaction(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.cfg.MaxBudgetTokens = 100
	s.tok = byteTokenizer{}

	s.messages = []Message{
		{Role: RoleUser, Content: strings.Repeat("a", 60)},
		{Role: RoleAssistant, Content: strings.Repeat("b", 60)},
		{Role: RoleUser, Content: "当前"},
	}
	// 未注入时 len/4 = 30 < 100 不该压缩；注入后 123 > 100 必须压缩。
	if got := s.countMessages(s.messages); got < 100 {
		t.Fatalf("注入的计数器应给出 >100, got %d", got)
	}
	if n := s.compact(context.Background()); n == 0 {
		t.Fatal("精确计数超预算时应触发压缩")
	}
	if !strings.Contains(s.TokenizerName(), "bytes") {
		t.Fatalf("TokenizerName 应反映注入实现: %q", s.TokenizerName())
	}
}

// 注入的 tokenizer 失败时回退估算，不能让整个回合挂掉。
type brokenTokenizer struct{}

func (brokenTokenizer) Count(string) (int, bool) { return 0, false }
func (brokenTokenizer) Name() string             { return "test:broken" }

func TestBrokenTokenizerFallsBackToEstimate(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.tok = brokenTokenizer{}
	if got := s.countText("abcdefgh"); got != 2 {
		t.Fatalf("应回退 len/4=2, got %d", got)
	}
	if _, err := s.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("tokenizer 坏掉不该影响对话: %v", err)
	}
}

func TestCountAIIncludesSystem(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.System = strings.Repeat("s", 40) // len/4 = 10，+4 结构开销
	got := s.countAI([]ai.Message{{Role: "system", Content: s.System}})
	if got != 14 {
		t.Fatalf("应 10+4=14, got %d", got)
	}
}
