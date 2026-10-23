package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
)

func testManifest(id string) manifest.Manifest {
	return manifest.Manifest{
		Identity: manifest.ModuleIdentity{ID: id, Name: id, Type: "simulation"},
		Version:  "1.0",
	}
}

func TestRegistryRegisterExecuteUnload(t *testing.T) {
	r := NewRegistry()
	p := &Func{
		M:    testManifest("echo"),
		Acts: []string{"ping"},
		Run: func(_ context.Context, action string, params map[string]any) (any, error) {
			return action + ":pong", nil
		},
	}
	if err := r.Register(context.Background(), nil, p); err != nil {
		t.Fatal(err)
	}
	got, err := r.Execute(context.Background(), "echo", "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ping:pong" {
		t.Fatalf("got %v", got)
	}
	list := r.List()
	if len(list) != 1 || list[0].ID != "echo" || len(list[0].Actions) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if err := r.Unload("echo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("echo"); ok {
		t.Fatal("卸载后仍能取到插件")
	}
}

func TestExternalEcho(t *testing.T) {
	script := `while IFS= read -r line; do printf '{"ok":true,"result":"pong"}\n'; done`
	p := NewExternal(Spec{
		Manifest: testManifest("ext"),
		Command:  "sh",
		Args:     []string{"-c", script},
		Actions:  []string{"ping"},
	})
	if err := p.Init(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	got, err := p.Execute(context.Background(), "ping", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != "pong" {
		t.Fatalf("got %v", got)
	}
}

func TestParseFileFlatAndNested(t *testing.T) {
	flat := []byte(`{"identity":{"id":"flat","name":"Flat","type":"simulation"},"version":"1.0","command":"python3","args":["p.py"],"actions":["run"]}`)
	f, err := ParseFile(flat)
	if err != nil {
		t.Fatal(err)
	}
	if f.Manifest.Identity.ID != "flat" || f.Command != "python3" || len(f.Args) != 1 {
		t.Fatalf("flat = %+v", f)
	}

	nested := []byte(`{"manifest":{"identity":{"id":"nested","name":"Nested","type":"simulation"},"version":"1.0"},"command":"node","args":["p.js"]}`)
	f, err = ParseFile(nested)
	if err != nil {
		t.Fatal(err)
	}
	if f.Manifest.Identity.ID != "nested" || f.Command != "node" {
		t.Fatalf("nested = %+v", f)
	}

	if _, err := ParseFile([]byte(`{"command":"x"}`)); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("缺 identity 应报错, got %v", err)
	}
}
