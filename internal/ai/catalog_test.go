package ai

import "testing"

// TestBuiltinsCount 保证内置 provider 目录达到 200+（用户扩展目标）。
// 数据来自 hermes 移植 + 确知真实 OpenAI 兼容端点 + 聚合器，无编造 base_url。
func TestBuiltinsCount(t *testing.T) {
	ps := Builtins()
	if len(ps) < 200 {
		t.Fatalf("内置 provider 数量 = %d，期望 >= 200", len(ps))
	}
	// 注册后注册表也应 >= 200（同名覆盖不丢条目）。
	if err := RegisterBuiltins(); err != nil {
		t.Fatalf("RegisterBuiltins 失败: %v", err)
	}
	if got := len(List()); got < 200 {
		t.Fatalf("注册后 List() = %d，期望 >= 200", got)
	}
}

// TestBuiltinNamesLowercase 保证所有 Name 全小写（注册表键约束）。
func TestBuiltinNamesLowercase(t *testing.T) {
	for _, p := range Builtins() {
		for _, r := range p.Name {
			if r >= 'A' && r <= 'Z' {
				t.Fatalf("provider %q 的 Name 含大写，必须全小写", p.Name)
			}
		}
	}
}
