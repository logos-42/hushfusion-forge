package agent

// serve.go —— stdio JSONL 会话服务器（forge agent serve 的 runtime 侧）。
//
// 形态（复用世界协议的形态）：stdin 每行一个请求，stdout 每行一个响应。
// TUI（另一项目）经管道连上来做连续对话 —— 会话按 session 字段区分，驻留进程内。
//
// 请求：
//
//	{"msg": "你好", "session": "id"?}        发起/继续对话
//	{"reset": true, "session": "id"?}       清空某会话
//	{"quit": true}                           优雅退出
//
// 响应：
//
//	{"session", "role", "content", "tool_calls"}
//	{"session", "error": "..."}               fail-closed 错误（会话保留，可继续）
//
// 收敛约定：单用户消息一轮；工具往返由 Session.Turn 内部循环。每一行请求至多
// 产生一行最终响应（工具中间过程不单独发流 —— 非流式诚实边界，见设计文档 §8）。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ServeRequest 是一行 stdio 请求。
type ServeRequest struct {
	Msg     string `json:"msg,omitempty"`
	Session string `json:"session,omitempty"`
	Reset   bool   `json:"reset,omitempty"`
	Quit    bool   `json:"quit,omitempty"`
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
	content, err := s.Turn(ctx, req.Msg)
	if err != nil {
		return ServeResponse{Session: id, Error: err.Error()}, nil
	}
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
