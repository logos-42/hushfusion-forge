package kernel

import (
	"fmt"
	"time"
)

// transport 移植: 通信入口端口 (send / broadcast / peers)。
// 与 run-lifecycle 同一套端口写法: 端口注入 · 未注入即拒 · 一律返回结果对象不抛。

var transportOps = map[string]bool{"send": true, "broadcast": true, "peers": true}
var transportNeedsTarget = map[string]bool{"send": true, "broadcast": false, "peers": false}

// SubmitTransport 提交一次通信动作。唯一入口 —— 校验 → 派发到 port → 记审计。
func SubmitTransport(req map[string]any, ports *Ports, audit *auditLog) map[string]any {
	op := asString(req["op"])
	origin := asString(req["origin"])
	peerID := asString(req["peerId"])
	target := peerID
	if target == "" {
		target = "(all)"
	}
	finish := func(ok bool, detail, port string, result any) map[string]any {
		audit.push(auditEntry{
			At: time.Now().UnixMilli(), Op: op, Origin: orAnon(origin), Target: target, OK: ok, Detail: detail,
		})
		out := map[string]any{"ok": ok, "op": op, "via": "kernel-transport", "port": port, "result": result}
		if detail != "" {
			out["detail"] = detail
		}
		return out
	}
	if !transportOps[op] {
		return finish(false, fmt.Sprintf("未知通信动作: %s", op), "", nil)
	}
	if origin == "" {
		return finish(false, "缺少 origin (审计要求: 不许匿名发送)", "", nil)
	}
	if transportNeedsTarget[op] && peerID == "" {
		return finish(false, fmt.Sprintf("%s 缺少 peerId", op), "", nil)
	}
	kind := asString(req["kind"])
	if kind == "" {
		kind = "message"
	}
	payload := req["payload"]
	if payload == nil {
		payload = ""
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
	case "send":
		if ports.Send == nil {
			return finish(false, "port 未注入: send", "", nil)
		}
		return dispatch("send", func() (any, error) { return ports.Send(peerID, kind, payload) })
	case "broadcast":
		if ports.Broadcast == nil {
			return finish(false, "port 未注入: broadcast", "", nil)
		}
		return dispatch("broadcast", func() (any, error) { return ports.Broadcast(kind, payload) })
	case "peers":
		if ports.Peers == nil {
			return finish(false, "port 未注入: peers", "", nil)
		}
		return dispatch("peers", func() (any, error) { return ports.Peers() })
	}
	return finish(false, fmt.Sprintf("未处理的通信动作: %s", op), "", nil)
}
