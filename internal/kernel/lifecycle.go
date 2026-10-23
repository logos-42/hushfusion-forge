package kernel

import (
	"fmt"
	"time"
)

// run-lifecycle 移植: 回合事实写口 (start-run / record-step / finish-run / save-checkpoint)。
// 形状照抄原内核: 端口注入 · 未注入即拒 · 一律返回结果对象不抛 · 审计可读。

var runLifecycleOps = map[string]bool{
	"start-run": true, "record-step": true, "finish-run": true, "save-checkpoint": true,
}

// 哪些操作必须有 runId (start-run 例外: 它的 runId 是结果, 不是入参)。
var runLifecycleNeedsRunID = map[string]bool{
	"start-run": false, "record-step": true, "finish-run": true, "save-checkpoint": true,
}

type auditEntry struct {
	At     int64  `json:"at"`
	Op     string `json:"op"`
	Origin string `json:"origin"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

const auditMax = 200

type auditLog struct {
	entries []auditEntry
}

func (a *auditLog) push(e auditEntry) {
	a.entries = append(a.entries, e)
	if len(a.entries) > auditMax {
		a.entries = a.entries[len(a.entries)-auditMax:]
	}
}

func portRefusal(res any) (string, bool) {
	m, ok := res.(map[string]any)
	if !ok {
		return "", false
	}
	if v, exists := m["ok"]; exists {
		if b, isBool := v.(bool); isBool && !b {
			reason, _ := m["reason"].(string)
			if reason == "" {
				reason = "(未给原因)"
			}
			return "端口拒绝: " + reason, true
		}
	}
	return "", false
}

// SubmitRunLifecycle 提交一条生命周期写入。唯一入口 —— 校验 → 派发到 port → 记审计;
// 一律返回结果对象, 不抛。
func SubmitRunLifecycle(req map[string]any, ports *Ports, audit *auditLog) map[string]any {
	op := asString(req["op"])
	origin := asString(req["origin"])
	runID := asString(req["runId"])
	target := runID
	if target == "" {
		target = "(new)"
	}
	finish := func(ok bool, detail, port string, result any) map[string]any {
		audit.push(auditEntry{
			At: time.Now().UnixMilli(), Op: op, Origin: orAnon(origin), Target: target, OK: ok, Detail: detail,
		})
		out := map[string]any{"ok": ok, "op": op, "via": "kernel-run-lifecycle", "port": port, "result": result}
		if detail != "" {
			out["detail"] = detail
		}
		return out
	}
	if !runLifecycleOps[op] {
		return finish(false, fmt.Sprintf("未知写入类型: %s", op), "", nil)
	}
	if origin == "" {
		return finish(false, "缺少 origin (审计要求: 不许匿名写入)", "", nil)
	}
	if runLifecycleNeedsRunID[op] && runID == "" {
		return finish(false, fmt.Sprintf("%s 缺少 runId", op), "", nil)
	}
	payload := req["payload"]
	if payload == nil {
		payload = map[string]any{}
	}
	dispatch := func(portName string, fn func() (any, error)) map[string]any {
		res, err := fn()
		if err != nil {
			return finish(false, fmt.Sprintf("%s 失败: %s", portName, truncate(err.Error(), 160)), "", nil)
		}
		if refusal, refused := portRefusal(res); refused {
			return finish(false, refusal, portName, res)
		}
		return finish(true, "", portName, res)
	}
	switch op {
	case "start-run":
		if ports.StartRun == nil {
			return finish(false, "port 未注入: startRun", "", nil)
		}
		return dispatch("startRun", func() (any, error) { return ports.StartRun(payload) })
	case "record-step":
		if ports.RecordStep == nil {
			return finish(false, "port 未注入: recordStep", "", nil)
		}
		return dispatch("recordStep", func() (any, error) { return ports.RecordStep(runID, payload) })
	case "finish-run":
		if ports.FinishRun == nil {
			return finish(false, "port 未注入: finishRun", "", nil)
		}
		return dispatch("finishRun", func() (any, error) { return ports.FinishRun(runID, payload) })
	case "save-checkpoint":
		if ports.SaveCheckpoint == nil {
			return finish(false, "port 未注入: saveCheckpoint", "", nil)
		}
		return dispatch("saveCheckpoint", func() (any, error) { return ports.SaveCheckpoint(runID, payload) })
	}
	return finish(false, fmt.Sprintf("未处理的写入类型: %s", op), "", nil)
}

// AssertLifecycleOk: 生命周期写入必须落盘, 拿不到 ok 就抛 (run-store 是 strict)。
func AssertLifecycleOk(out map[string]any) error {
	if ok, _ := out["ok"].(bool); !ok {
		detail, _ := out["detail"].(string)
		if detail == "" {
			detail = "未知原因"
		}
		return fmt.Errorf("[kernel-run-lifecycle] %s 未落盘: %s (run-store 是 strict: 写不进 = 这次运行在事实层面不存在, 不许静默继续)", asString(out["op"]), detail)
	}
	return nil
}

func orAnon(s string) string {
	if s == "" {
		return "(anonymous)"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
