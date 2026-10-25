package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/agent"
)

// TestDumpToolSpecs 导出实际注册的全部工具 schema（DUMP_TOOLS=<path> 才启用），
// 用于排查供应商 400: tools.parameters invalid。
func TestDumpToolSpecs(t *testing.T) {
	out := os.Getenv("DUMP_TOOLS")
	if out == "" {
		t.Skip("需要 DUMP_TOOLS=<path>")
	}
	base := setupTempOS(t)
	s := agent.NewSession(agent.DefaultConfig(), nil, agent.NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	registerProjectTools(s, base)
	specs := s.ToolSpecs()
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("dumped %d specs to %s", len(specs), out)
}
