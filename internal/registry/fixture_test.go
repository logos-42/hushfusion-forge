package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmitSchemaParityFixture 通过真实的写入方产出一个小的 registry, 这样独立的
// Python 检查器 (python/aux/schema_check.py, 验收门 G9) 可以对 Go 实际写出的字节
// 运行, 而不是对 Go 自己以为的 schema 运行。Python 检查器从外部重新声明了字段名、
// metric 键、param 块以及 “design_id == experiment_id 的 D%04d” 规则, 所以这里改名
// 会被一个与 Go 不共享任何代码的东西抓住。
//
// 除非设置了 FORGE_FIXTURE_DIR, 否则它会被跳过, 因此 `go test ./...` 永远不会写到
// 临时目录之外。用法:
//
//	FORGE_FIXTURE_DIR=/tmp/forge-fix go test ./internal/registry/ -run TestEmitSchemaParityFixture
//	python3 python/aux/schema_check.py /tmp/forge-fix/registry.jsonl
func TestEmitSchemaParityFixture(t *testing.T) {
	dir := os.Getenv("FORGE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set FORGE_FIXTURE_DIR=<dir> to emit registry.jsonl for the Python schema-parity gate")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	reg, err := Open(filepath.Join(dir, "registry.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 一个根、一个子节点、一个孙节点: 这个夹具演练了 parent_design、lineage 边
	// 以及 id/design_id 的配对, 而不只是一个平铺列表。
	root := recordFixture(-0.2905708161, "human_baseline")
	root.Feasible = false
	if _, err := reg.AppendAssign(root); err != nil {
		t.Fatalf("AppendAssign root: %v", err)
	}
	for i, parent := range []string{"D0001", "D0002"} {
		rec := recordFixture(float64(i)*0.5, "evolution")
		rec.ParentDesign = parent
		rec.Generation = i + 1
		rec.EvalIndex = i
		rec.Note = "fixture child"
		if _, err := reg.AppendAssign(rec); err != nil {
			t.Fatalf("AppendAssign child %d: %v", i, err)
		}
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v; want a clean registry", problems, err)
	}
	if reg.Len() != 3 {
		t.Fatalf("Len = %d, want 3", reg.Len())
	}
}
