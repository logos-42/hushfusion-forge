package dchat

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestOrbitDBLive 连真实依赖跑一次 orbitdb send（DCHAT_LIVE=1 才启用）。
func TestOrbitDBLive(t *testing.T) {
	if os.Getenv("DCHAT_LIVE") == "" {
		t.Skip("需要 DCHAT_LIVE=1（会真实启动 IPFS，冷启动 30-90s）")
	}
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if _, err := m.Status(ctx, "orbitdb"); err != nil {
		t.Fatalf("status: %v", err)
	}
	res, err := m.Send(ctx, "orbitdb", "forge-room", "go-live-test")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("send: %v", res)
}
