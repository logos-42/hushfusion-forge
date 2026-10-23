package agent

// serve.go —— stdio JSONL 会话服务器（forge agent serve 的 runtime 侧）。
//
// 形态（复用世界协议的形态）：stdin 每行一个请求，stdout 每行一个响应。
// TUI（另一项目）经管道连上来做连续对话 —— 会话按 session 字段区分，驻留进程内。
//
// 请求：
//
//	{"msg": "你好", "session": "id"?}        发起/继续对话
//	{"msg": "...", "stream": true}           同上，但逐增量多行输出（见下）
//	{"reset": true, "session": "id"?}       清空某会话
//	{"quit": true}                           优雅退出
//
// 响应（非流式，向后兼容）：
//
//	{"session", "role", "content", "tool_calls"}
//	{"session", "error": "..."}               fail-closed 错误（会话保留，可继续）
//
// 流式（仅当请求带 "stream":true）：一行请求产出**多行**：
//
//	{"session", "type":"delta", "text":"..."}     文本增量（可多条）
//	{"session", "type":"tool_delta", "index":N, "name":.., "args":..}  工具调用增量
//	{"session", "type":"done", "content":"...", "finish":.., "usage":..}  最终一行
//	{"session", "type":"error", "error":"..."}   失败
//
// 非流式契约一行一响应完全保留；流式是 opt-in 超集，不破坏旧客户端。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// ServeRequest 是一行 stdio 请求。
type ServeRequest struct {
	Msg     string `json:"msg,omitempty"`
	Session string `json:"session,omitempty"`
	Reset   bool   `json:"reset,omitempty"`
	Quit    bool   `json:"quit,omitempty"`
	Stream  bool   `json:"stream,omitempty"` // 仅对 msg 请求生效：逐增量多行输出
}

// ServeResponse 是一行 stdio 响应。
type ServeResponse struct {
	Session string `json:"session,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	Reset   bool   `json:"reset,omitempty"`
	Quit    bool   `json:"quit,omitempty"`
	Error   string `json:"error,omitempty"`
	// ToolCalls 是模型在最终回复前发起的工具调用摘要（TUI 展示用；非最终回复才有）。
	ToolCalls []string `json:"tool_calls,omitempty"`
}

// ServeHandler 是一次 serve 的全部状态：会话表 + 工具/聊天装配。
// 由 cmd 层构造（装配真实现），agent 包只提供纯状态机（可测）。
type ServeHandler struct {
	// NewSession 为每个 session id 构造会话（缺省 session 只构造一次）。
	NewSession func(sessionID string) *Session
	// sessions 是活跃会话表。
	sessions map[string]*Session
	// MaxSessions 是会话数上限（防泄漏；0 = 不限）。
	MaxSessions int
	// AfterTurn 可选：每回合/重置后回调（持久化消息栈用，见 cmd 层 --state-dir）。
	// 回调里的错误由调用方自己报（本包只负责"在该调的时候调"，不吞不猜）。
	AfterTurn func(sessionID string, s *Session)
}

// NewServeHandler 构造服务器（NewSession 为 nil 时用 defaultNewSession，不可用）。
func NewServeHandler(newSession func(string) *Session) *ServeHandler {
	return &ServeHandler{
		NewSession:  newSession,
		sessions:    map[string]*Session{},
		MaxSessions: 32,
	}
}

func (h *ServeHandler) get(sessionID string) (*Session, bool) {
	if sessionID == "" {
		sessionID = "default"
	}
	s, ok := h.sessions[sessionID]
	if !ok {
		if h.MaxSessions > 0 && len(h.sessions) >= h.MaxSessions {
			return nil, false
		}
		s = h.NewSession(sessionID)
		h.sessions[sessionID] = s
	}
	return s, true
}

// Handle 处理一行请求（无并发：调用方串行喂）。
func (h *ServeHandler) Handle(ctx context.Context, line []byte) (ServeResponse, error) {
	var req ServeRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return ServeResponse{}, fmt.Errorf("agent serve: 非法请求 JSON: %w", err)
	}
	id := req.Session
	if id == "" {
		id = "default"
	}
	if req.Quit {
		return ServeResponse{Session: id, Quit: true}, nil
	}
	if req.Reset {
		if s, ok := h.sessions[id]; ok {
			s.Reset()
			h.afterTurn(id, s) // 重置也要落盘：否则重启后"已清空"的历史又回来了
		}
		return ServeResponse{Session: id, Reset: true}, nil
	}
	s, ok := h.get(id)
	if !ok {
		return ServeResponse{}, fmt.Errorf("agent serve: 会话数已达上限 %d", h.MaxSessions)
	}
	if strings.TrimSpace(req.Msg) == "" {
		return ServeResponse{Session: id, Error: "msg 为空"}, nil
	}
	if req.Stream {
		// 流式：调用方提供一个 writer（见 Handle 调用处），逐增量多行输出。
		return ServeResponse{}, errStreamUnsupported
	}
	content, err := s.Turn(ctx, req.Msg)
	if err != nil {
		return ServeResponse{Session: id, Error: err.Error()}, nil
	}
	h.afterTurn(id, s)
	resp := ServeResponse{Session: id, Role: "assistant", Content: content}
	// 工具调用摘要：把最后一次 assistant 消息里的工具名带出来（TUI 展示已执行了什么）。
	if hist := s.History(); len(hist) > 0 {
		for _, m := range hist {
			if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					resp.ToolCalls = append(resp.ToolCalls, tc.Function.Name)
				}
			}
		}
	}
	return resp, nil
}

// errStreamUnsupported 是占位：流式走 HandleStream 而非 Handle，不应以单行返回。
var errStreamUnsupported = fmt.Errorf("agent serve: 流式请求需走 HandleStream")

// HandleStream 处理流式 msg 请求：把增量直接写成多行 JSON 到 w（每行一个事件）。
// 调用方负责在之后 w.Flush()。非流式请求仍走 Handle。
func (h *ServeHandler) HandleStream(ctx context.Context, req ServeRequest, w io.Writer) error {
	id := req.Session
	if id == "" {
		id = "default"
	}
	enc := json.NewEncoder(w)
	onDelta := func(ev ai.StreamEvent) {
		switch ev.Type {
		case ai.StreamContent:
			_ = enc.Encode(map[string]any{"session": id, "type": "delta", "text": ev.Text})
		case ai.StreamToolCall:
			_ = enc.Encode(map[string]any{
				"session": id, "type": "tool_delta",
				"index": ev.ToolIndex, "name": ev.ToolName, "args": ev.ToolArgs,
			})
		case ai.StreamDone:
			_ = enc.Encode(map[string]any{
				"session": id, "type": "done", "finish": ev.Finish, "usage": ev.Usage,
			})
		}
	}
	s, ok := h.get(id)
	if !ok {
		return fmt.Errorf("agent serve: 会话数已达上限 %d", h.MaxSessions)
	}
	content, err := s.TurnStream(ctx, req.Msg, onDelta)
	if err != nil {
		_ = enc.Encode(map[string]any{"session": id, "type": "error", "error": err.Error()})
		return nil
	}
	h.afterTurn(id, s)
	_ = enc.Encode(map[string]any{"session": id, "type": "done", "content": content})
	return nil
}

// afterTurn 是 AfterTurn 钩子的空安全包装。
func (h *ServeHandler) afterTurn(id string, s *Session) {
	if h.AfterTurn != nil && s != nil {
		h.AfterTurn(id, s)
	}
}

// Serve 是 stdio 主循环：读 stdin 每行 → Handle → 写 stdout 每行（JSON）。
// 关键纪律（学世界协议）：stdout 只放协议行，任何噪声（日志/警告）都写 stderr。
func (h *ServeHandler) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	w := bufio.NewWriter(out)
	defer w.Flush()
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		var head ServeRequest
		streaming := json.Unmarshal(line, &head) == nil && head.Stream && !head.Reset && !head.Quit
		if streaming {
			// 流式：逐增量多行输出，flush 后继续。
			if err := h.HandleStream(ctx, head, w); err != nil {
				_ = enc.Encode(ServeResponse{Error: err.Error()})
			}
			w.Flush()
			continue
		}
		resp, err := h.Handle(ctx, line)
		if err != nil {
			// 协议级失败（非法 JSON / 会话上限）— 写错误响应，继续读下一行。
			_ = enc.Encode(ServeResponse{Error: err.Error()})
			w.Flush()
			if isQuit(line) {
				return nil
			}
			continue
		}
		_ = enc.Encode(resp)
		w.Flush()
		if resp.Quit {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("agent serve: 读 stdin: %w", err)
	}
	return nil
}

// isQuit 判断一行请求是否带 quit（尽力而为：单行协议，不必完整解析）。
func isQuit(line []byte) bool {
	return strings.Contains(strings.ToLower(string(line)), "\"quit\":true") ||
		strings.Contains(strings.ToLower(string(line)), "\"quit\": true")
}

// RunServe 是 cmd 层直接用的便捷入口（in/out 缺省为 os.Stdin/os.Stdout）。
func RunServe(ctx context.Context, h *ServeHandler) error {
	return h.Serve(ctx, os.Stdin, os.Stdout)
}
