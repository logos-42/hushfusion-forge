package ha

import (
	"testing"
)

func TestHeartbeatRoundTrip(t *testing.T) {
	rec := MakeHeartbeat(RolePrimary, "host-a", 1234, []string{"h1", "h2"}, "0.1.0")
	raw := SerializeHeartbeat(rec)
	got, ok := ParseHeartbeat(raw)
	if !ok {
		t.Fatalf("ParseHeartbeat 应成功: %q", raw)
	}
	if got.Role != RolePrimary || got.PID != 1234 || got.KernelVersion != "0.1.0" || len(got.RunIDs) != 2 {
		t.Fatalf("反序列化字段丢失: %+v", got)
	}
}

func TestParseHeartbeatInvalid(t *testing.T) {
	if _, ok := ParseHeartbeat("not json"); ok {
		t.Fatal("非法 JSON 应 ok=false")
	}
	if _, ok := ParseHeartbeat(`{"pid":1}`); ok {
		t.Fatal("无 role 应 ok=false")
	}
}

func TestIsStale(t *testing.T) {
	now := int64(100000)
	fresh := HeartbeatRecord{Role: RolePrimary, LastBeat: now - 1000}  // 1s 前
	stale := HeartbeatRecord{Role: RolePrimary, LastBeat: now - 6000} // 6s 前
	if IsStale(fresh, now, STALE_MS) {
		t.Fatal("1s 前不应过期")
	}
	if !IsStale(stale, now, STALE_MS) {
		t.Fatal("6s 前应过期")
	}
}

func TestDecideTakeover(t *testing.T) {
	now := int64(100000)
	stale := &HeartbeatRecord{Role: RolePrimary, LastBeat: now - 6000}

	if take, _ := DecideTakeover(nil, now, 0); !take {
		t.Fatal("primary 缺失应接管")
	}
	if take, r := DecideTakeover(stale, now, 0); !take || r != "primary-heartbeat-stale" {
		t.Fatalf("primary 过期应接管, got %v %q", take, r)
	}
	fresh := &HeartbeatRecord{Role: RolePrimary, LastBeat: now - 1000}
	if take, _ := DecideTakeover(fresh, now, 0); take {
		t.Fatal("primary 健康不应接管")
	}
}
