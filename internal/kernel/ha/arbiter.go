// Package ha 是双核冗余的心跳仲裁层（对齐 bolloon kernel 的 src/kernel/ha/）。
//
// 设计纪律（与 bolloon 一致）：
//   - 所有仲裁逻辑都是纯函数（Parse/IsStale/DecideTakeover），不碰 I/O，可单测；
//   - 写/读心跳经端口注入（文件路径或函数），未注入即不动作（fail-closed）；
//   - 双核 = primary + secondary 两个内核实例同时活着，secondary 看 primary 心跳，
//     缺失/过期即接管（heartbeat arbitration）。
package ha

import (
	"encoding/json"
	"time"
)

// 角色与默认心跳参数（对齐 bolloon ha-arbiter 的 BEAT_MS/STALE_MS）。
const (
	RolePrimary   = "primary"
	RoleSecondary = "secondary"

	BEAT_MS  = 1000
	STALE_MS = 5000
)

// HeartbeatRecord 是双核冗余里的一次心跳：
// 某个 kernel 实例的角色、进程、承载的 run 与内核版本。
type HeartbeatRecord struct {
	PID           int      `json:"pid"`
	Host          string   `json:"host"`
	Role          string   `json:"role"` // "primary" | "secondary"
	StartedAt     int64    `json:"startedAt"`
	LastBeat      int64    `json:"lastBeat"`
	RunIDs        []string `json:"runIds"`
	KernelVersion string   `json:"kernelVersion"`
}

// MakeHeartbeat 构造一条当前时刻的心跳。
func MakeHeartbeat(role, host string, pid int, runIDs []string, version string) HeartbeatRecord {
	now := time.Now().UnixMilli()
	return HeartbeatRecord{
		PID:           pid,
		Host:          host,
		Role:          role,
		StartedAt:     now,
		LastBeat:      now,
		RunIDs:        runIDs,
		KernelVersion: version,
	}
}

// SerializeHeartbeat 序列化为单行 JSON（用于落盘心跳文件）。
func SerializeHeartbeat(rec HeartbeatRecord) string {
	b, _ := json.Marshal(rec)
	return string(b)
}

// ParseHeartbeat 解析心跳；非法/无 role 返回 ok=false。
func ParseHeartbeat(raw string) (HeartbeatRecord, bool) {
	var rec HeartbeatRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return HeartbeatRecord{}, false
	}
	if rec.Role == "" {
		return HeartbeatRecord{}, false
	}
	return rec, true
}

// IsStale 心跳是否过期（距 LastBeat 超过 staleMs）。
func IsStale(rec HeartbeatRecord, now int64, staleMs int) bool {
	if staleMs <= 0 {
		staleMs = STALE_MS
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return now-rec.LastBeat > int64(staleMs)
}

// DecideTakeover 是纯函数仲裁：secondary 是否应接管。
//   - primary == nil            => 接管 (primary-heartbeat-missing)
//   - primary 心跳过期          => 接管 (primary-heartbeat-stale)
//   - 否则不接管。
func DecideTakeover(primary *HeartbeatRecord, now int64, staleMs int) (takeover bool, reason string) {
	if staleMs <= 0 {
		staleMs = STALE_MS
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	if primary == nil {
		return true, "primary-heartbeat-missing"
	}
	if IsStale(*primary, now, staleMs) {
		return true, "primary-heartbeat-stale"
	}
	return false, ""
}
