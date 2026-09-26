package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmitSchemaParityFixture writes a small registry through the REAL writer so
// that the independent Python checker (python/aux/schema_check.py, acceptance
// gate G9) can be run against Go's actual bytes instead of against Go's idea of
// its own schema. The Python checker re-states the field names, the metric keys,
// the param block and the "design_id == D%04d of experiment_id" rule from the
// outside, so a rename here would be caught by something that shares no code
// with Go.
//
// It is skipped unless FORGE_FIXTURE_DIR is set, so `go test ./...` never writes
// outside its temp directories. Usage:
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
	// A root, a child and a grandchild: the fixture exercises parent_design, the
	// lineage edge and the id/design_id pairing, not just a flat list.
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
