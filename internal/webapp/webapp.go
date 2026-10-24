// Package webapp 是 HUSHFUSION Forge 的桌面/Web 外壳：本地 HTTP 服务 +
// 嵌入式前端。第一版聚焦“主对话窗口 + 侧边栏（智能体/会话/账号/自动更新）”，
// 不引入重型桌面框架；打包产物是本机二进制 + 内嵌 Web UI。
package webapp

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/identity"
)

// Profile 是账号（侧边栏左下角）。
type Profile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	DID    string `json:"did"`
}

// Agent 是一个智能体身份。
type Agent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Avatar    string    `json:"avatar"`
	DID       string    `json:"did"`
	Persona   string    `json:"persona,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Message 是对话消息。
type Message struct {
	Role   string    `json:"role"`
	Text   string    `json:"text"`
	Images []string  `json:"images,omitempty"`
	Time   time.Time `json:"time"`
}

// Session 是会话栏里的一条会话（归属某个智能体）。
type Session struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
}

// Settings 是模型与工具设置（侧边栏/主窗口可选）。
type Settings struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools,omitempty"` // 需要放行的写工具；读工具默认可用
}

// State 是落盘状态（~/.forge/web/state.json）。
type State struct {
	Profile         Profile   `json:"profile"`
	Agents          []Agent   `json:"agents"`
	Sessions        []Session `json:"sessions"`
	ActiveAgentID   string    `json:"active_agent_id"`
	ActiveSessionID string    `json:"active_session_id"`
	Settings        Settings  `json:"settings"`
}

// Secret 是按 provider 保存的密钥/端点（单独文件 0600，绝不随 state 返回）。
type Secret struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// UpdateInfo 是侧边栏更新按钮状态。
type UpdateInfo struct {
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url,omitempty"`
	AssetURL  string `json:"asset_url,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// Server 是 Web 外壳。
type Server struct {
	addr        string
	dir         string
	root        string // workspace 根目录（工具/共享配置的锚点）
	osDir       string // <root>/os 或 HUSHFUSION_OS_DIR
	mu          sync.Mutex
	st          State
	http        *http.Server
	agentSesses map[string]*agent.Session
	secrets     map[string]Secret
}

const (
	stateFile   = "state.json"
	secretsFile = "secrets.json"
	maxAvatar   = 128 * 1024
	maxImage    = 1024 * 1024
	repoSlug    = "logos-42/hushfusion-forge"
)

// writeTools 是 Web 外壳里可放行的写工具（读工具默认可用）。
var writeTools = []string{"shell", "mem.set", "mem.reset", "tasks.add", "tasks.set_state"}

// New 构造服务器并加载/初始化状态。
func New(addr, dir string) (*Server, error) {
	root, _ := os.Getwd()
	return NewWithRoot(addr, dir, root)
}

// NewWithRoot 指定 workspace 根目录；Web/App/CLI/TUI 共用 <os>/forge.config.json。
func NewWithRoot(addr, dir, root string) (*Server, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".forge", "web")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		root, _ = os.Getwd()
	}
	osDir := strings.TrimSpace(os.Getenv("HUSHFUSION_OS_DIR"))
	if osDir == "" {
		osDir = filepath.Join(root, "os")
	}
	s := &Server{addr: addr, dir: dir, root: root, osDir: osDir,
		agentSesses: map[string]*agent.Session{}, secrets: map[string]Secret{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.loadSecrets(); err != nil {
		return nil, err
	}
	s.applySharedConfig() // 共享配置优先于 Web 自己的旧 state
	s.normalize()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("POST /api/agents", s.handleCreateAgent)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat/stream", s.handleChatStream)
	mux.HandleFunc("POST /api/profile", s.handleProfile)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/update/apply", s.handleUpdateApply)
	mux.Handle("/", http.FileServer(http.FS(assets)))
	s.http = &http.Server{Addr: addr, Handler: security(mux)}
	return s, nil
}

// Addr 返回监听地址。
func (s *Server) Addr() string { return s.addr }

// ListenAndServe 启动服务（阻塞）。
func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }

// Shutdown 优雅停止。
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 本地外壳：只允许本机与同局域网访问由调用方用 --addr 决定；
		// 这里统一加基础安全头，避免内嵌页面被当成可信源滥用。
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) load() error {
	b, err := os.ReadFile(filepath.Join(s.dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return s.initState()
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return fmt.Errorf("webapp: 状态文件损坏: %w", err)
	}
	if len(s.st.Agents) == 0 {
		return s.initState()
	}
	return nil
}

// normalize 兼容旧状态文件：补默认模型/工具/头像。
func (s *Server) normalize() {
	if !strings.HasPrefix(s.st.Profile.Avatar, "data:image/") {
		s.st.Profile.Avatar = svgAvatar("forge-user", firstRune(s.st.Profile.Name))
	}
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_AI_PROVIDER")); v != "" {
		s.st.Settings.Provider = v // CLI/TUI 注入的共享 provider 优先
	}
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_AI_MODEL")); v != "" {
		s.st.Settings.Model = v
	}
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_AI_BASE_URL")); v != "" {
		sec := s.secrets[s.st.Settings.Provider]
		sec.BaseURL = v
		s.secrets[s.st.Settings.Provider] = sec
	}
	if s.st.Settings.Provider == "" {
		s.st.Settings.Provider = defaultProvider()
	}
	if s.st.Settings.Model == "" {
		if p := ai.Lookup(s.st.Settings.Provider); p != nil {
			s.st.Settings.Model = p.DefaultModel
		}
	}
	for i := range s.st.Agents {
		if !strings.HasPrefix(s.st.Agents[i].Avatar, "data:image/") {
			s.st.Agents[i].Avatar = svgAvatar(s.st.Agents[i].ID, firstRune(s.st.Agents[i].Name))
		}
	}
	_ = s.save()
}

func firstRune(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "F"
	}
	return string(r[0])
}

// svgAvatar 生成一个好看的渐变 SVG 头像（data:image/svg+xml），不用 emoji。
func svgAvatar(seed, label string) string {
	h := 0
	for _, r := range seed {
		h = (h*31 + int(r)) % 360
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96" viewBox="0 0 96 96"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(%d,82%%,58%%)"/><stop offset="1" stop-color="hsl(%d,72%%,42%%)"/></linearGradient></defs><rect width="96" height="96" rx="48" fill="url(#g)"/><circle cx="70" cy="26" r="10" fill="rgba(255,255,255,.28)"/><text x="48" y="60" font-family="-apple-system,Segoe UI,Roboto,sans-serif" font-size="38" font-weight="600" fill="#fff" text-anchor="middle">%s</text></svg>`,
		h, (h+48)%360, escapeXML(label))
	return "data:image/svg+xml;utf8," + url.QueryEscape(svg)
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func defaultProvider() string {
	if v := envOr("HUSHFUSION_AI_PROVIDER", ""); v != "" {
		return v
	}
	_ = ai.RegisterBuiltins()
	for _, p := range ai.Builtins() {
		if ai.ResolveAPIKey(p) != "" {
			return p.Name
		}
	}
	return "zai"
}

func (s *Server) initState() error {
	id, err := identity.Generate()
	if err != nil {
		return err
	}
	now := time.Now()
	agent := Agent{
		ID:        "agent-1",
		Name:      "Forge 主智能体",
		Avatar:    svgAvatar("forge-agent", "F"),
		DID:       id.DID(),
		Persona:   "可控核聚变开发协作智能体；诚实、可验证、专注。",
		CreatedAt: now,
	}
	sess := Session{
		ID:        "sess-1",
		AgentID:   agent.ID,
		Title:     "新对话",
		CreatedAt: now,
	}
	provider := defaultProvider()
	model := ""
	if p := ai.Lookup(provider); p != nil {
		model = p.DefaultModel
	}
	s.st = State{
		Profile:         Profile{Name: "Forge 用户", Avatar: svgAvatar("forge-user", "F"), DID: id.DID()},
		Agents:          []Agent{agent},
		Sessions:        []Session{sess},
		ActiveAgentID:   agent.ID,
		ActiveSessionID: sess.ID,
		Settings:        Settings{Provider: provider, Model: model},
	}
	return s.save()
}

func (s *Server) save() error {
	b, err := json.MarshalIndent(&s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, stateFile))
}

func (s *Server) loadSecrets() error {
	b, err := os.ReadFile(filepath.Join(s.dir, secretsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.secrets)
}

func (s *Server) saveSecrets() error {
	b, err := json.MarshalIndent(&s.secrets, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, secretsFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, secretsFile))
}

// hasKey 判断 provider 是否已有可用 key（env 或本机保存的 secret；不验证有效性）。
func (s *Server) hasKey(provider string) bool {
	if sec, ok := s.secrets[provider]; ok && strings.TrimSpace(sec.APIKey) != "" {
		return true
	}
	if p := ai.Lookup(provider); p != nil {
		return ai.ResolveAPIKey(p) != ""
	}
	return false
}

func (s *Server) secretFor(provider string) Secret { return s.secrets[provider] }

// sharedConfig 是 <os>/forge.config.json 的读取视图（与 TUI/CLI 共用）。
type sharedConfig struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

func (s *Server) sharedConfigPath() string {
	return filepath.Join(s.osDir, "forge.config.json")
}

// applySharedConfig 让 Web 启动时优先复用 TUI/CLI/SDK 的模型配置与 key。
func (s *Server) applySharedConfig() {
	b, err := os.ReadFile(s.sharedConfigPath())
	if err != nil {
		return
	}
	var c sharedConfig
	if json.Unmarshal(b, &c) != nil {
		return
	}
	if c.Provider != "" {
		s.st.Settings.Provider = c.Provider
	}
	if c.Model != "" {
		s.st.Settings.Model = c.Model
	}
	if c.APIKey != "" || c.BaseURL != "" {
		s.secrets[c.Provider] = Secret{APIKey: c.APIKey, BaseURL: c.BaseURL}
	}
}

// saveSharedConfig 把 Web 里选的模型/端点/密钥写回共享配置，CLI/TUI 下次启动即复用。
func (s *Server) saveSharedConfig(settings Settings, apiKey, baseURL string) error {
	path := s.sharedConfigPath()
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m["provider"] = settings.Provider
	m["model"] = settings.Model
	if strings.TrimSpace(apiKey) != "" {
		m["api_key"] = strings.TrimSpace(apiKey)
	}
	if strings.TrimSpace(baseURL) != "" {
		m["base_url"] = strings.TrimSpace(baseURL)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.osDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (s *Server) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 浅拷贝足够（JSON 响应会重新序列化；避免调用方改内部切片）。
	out := s.st
	out.Agents = append([]Agent(nil), s.st.Agents...)
	out.Sessions = append([]Session(nil), s.st.Sessions...)
	return out
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	st := s.snapshot()
	s.writeJSON(w, map[string]any{
		"version": config.ForgeVersion,
		"state":   st,
		"update":  s.checkUpdateBestEffort(r.Context()),
	})
}

type createAgentReq struct {
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Persona string `json:"persona"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req createAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "智能体名字不能为空", http.StatusBadRequest)
		return
	}
	avatar := strings.TrimSpace(req.Avatar)
	if avatar == "" {
		avatar = svgAvatar(name+time.Now().String(), firstRune(name))
	}
	if !avatarOK(avatar) {
		http.Error(w, "头像非法（data:image，≤128KB）", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := identity.Generate()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	agent := Agent{
		ID:        fmt.Sprintf("agent-%d", time.Now().UnixNano()),
		Name:      name,
		Avatar:    avatar,
		DID:       id.DID(),
		Persona:   strings.TrimSpace(req.Persona),
		CreatedAt: time.Now(),
	}
	sess := Session{ID: fmt.Sprintf("sess-%d", time.Now().UnixNano()), AgentID: agent.ID, Title: "新对话", CreatedAt: time.Now()}
	s.st.Agents = append(s.st.Agents, agent)
	s.st.Sessions = append(s.st.Sessions, sess)
	s.st.ActiveAgentID = agent.ID
	s.st.ActiveSessionID = sess.ID
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"agent": agent, "session": sess})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	agentID := req.AgentID
	if agentID == "" {
		agentID = s.st.ActiveAgentID
	}
	if s.findAgent(agentID) == nil {
		http.Error(w, "智能体不存在", http.StatusBadRequest)
		return
	}
	sess := Session{ID: fmt.Sprintf("sess-%d", time.Now().UnixNano()), AgentID: agentID, Title: "新对话", CreatedAt: time.Now()}
	s.st.Sessions = append(s.st.Sessions, sess)
	s.st.ActiveAgentID = agentID
	s.st.ActiveSessionID = sess.ID
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, sess)
}

type chatReq struct {
	SessionID string   `json:"session_id"`
	AgentID   string   `json:"agent_id"`
	Message   string   `json:"message"`
	Images    []string `json:"images,omitempty"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" && len(req.Images) == 0 {
		http.Error(w, "消息或图片不能为空", http.StatusBadRequest)
		return
	}
	if err := imagesOK(req.Images); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sess := s.findSession(req.SessionID)
	if sess == nil {
		s.mu.Unlock()
		http.Error(w, "会话不存在", http.StatusBadRequest)
		return
	}
	agentID := req.AgentID
	if agentID == "" {
		agentID = sess.AgentID
	}
	agent := s.findAgent(agentID)
	if agent == nil {
		s.mu.Unlock()
		http.Error(w, "智能体不存在", http.StatusBadRequest)
		return
	}
	history := append([]Message(nil), sess.Messages...) // 不含本轮新 user，避免 Turn 重复追加
	sess.Messages = append(sess.Messages, Message{
		Role: "user", Text: msg, Images: append([]string(nil), req.Images...), Time: time.Now(),
	})
	if sess.Title == "新对话" && len([]rune(msg)) > 4 {
		sess.Title = string([]rune(msg)[:min(24, len([]rune(msg)))])
	}
	settings := s.st.Settings
	if err := s.save(); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()

	as, err := s.agentSession(settings, sess, *agent, history)
	if err != nil {
		s.writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	reply, err := as.Turn(r.Context(), textWithImages(Message{Role: "user", Text: msg, Images: req.Images}))
	if err != nil {
		s.writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Text: reply, Time: time.Now()})
	_ = s.save()
	s.mu.Unlock()
	s.writeJSON(w, map[string]any{"reply": reply})
}

// handleChatStream 是 SSE 版聊天：delta 事件逐字推送，done 事件给最终回复。
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" && len(req.Images) == 0 {
		http.Error(w, "消息或图片不能为空", http.StatusBadRequest)
		return
	}
	if err := imagesOK(req.Images); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sess := s.findSession(req.SessionID)
	if sess == nil {
		s.mu.Unlock()
		http.Error(w, "会话不存在", http.StatusBadRequest)
		return
	}
	agentID := req.AgentID
	if agentID == "" {
		agentID = sess.AgentID
	}
	ag := s.findAgent(agentID)
	if ag == nil {
		s.mu.Unlock()
		http.Error(w, "智能体不存在", http.StatusBadRequest)
		return
	}
	history := append([]Message(nil), sess.Messages...)
	sess.Messages = append(sess.Messages, Message{
		Role: "user", Text: msg, Images: append([]string(nil), req.Images...), Time: time.Now(),
	})
	if sess.Title == "新对话" && len([]rune(msg)) > 4 {
		sess.Title = string([]rune(msg)[:min(24, len([]rune(msg)))])
	}
	settings := s.st.Settings
	if err := s.save(); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	as, err := s.agentSession(settings, sess, *ag, history)
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	reply, err := as.TurnStream(r.Context(), textWithImages(Message{Role: "user", Text: msg, Images: req.Images}), func(ev ai.StreamEvent) {
		if ev.Type == ai.StreamContent && ev.Text != "" {
			send("delta", map[string]string{"text": ev.Text})
		}
	})
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Text: reply, Time: time.Now()})
	_ = s.save()
	s.mu.Unlock()
	send("done", map[string]string{"reply": reply})
}

// agentSession 为 Web 会话构造/复用 agent Runtime（带工具；写工具按设置放行）。
// history 是「本轮 user 之前」的历史，避免 Turn/TurnStream 再追加时重复。
func (s *Server) agentSession(settings Settings, sess *Session, ag Agent, history []Message) (*agent.Session, error) {
	if s.agentSesses == nil {
		s.agentSesses = map[string]*agent.Session{}
	}
	if as, ok := s.agentSesses[sess.ID]; ok {
		return as, nil
	}
	root := strings.TrimSpace(s.root)
	if root == "" {
		root, _ = os.Getwd()
	}
	cfg := agent.DefaultConfig()
	cfg.Provider, cfg.Model = settings.Provider, settings.Model
	cfg.MaxBudgetTokens = agent.BudgetFromEnv(agent.BudgetForModel(settings.Model, 0.6))
	cfg.Root = root
	opts := func() ai.ClientOptions {
		sec := s.secretFor(settings.Provider)
		base := firstNonEmpty(sec.BaseURL, os.Getenv("HUSHFUSION_AI_BASE_URL"))
		return ai.ClientOptions{BaseURL: base, APIKey: sec.APIKey}
	}
	chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		return ai.Chat(ctx, settings.Provider, req, opts())
	}
	as := agent.NewSession(cfg, chat, agent.NewRegistry(), systemPrompt(ag))
	as.SetStreamChat(func(ctx context.Context, req ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error) {
		return ai.ChatStream(ctx, settings.Provider, req, on, opts())
	})
	_ = as.RegisterBuiltinTools(nil) // read/ls/shell/mem/tasks；skill.view 无技能目录不注册
	as.AllowWrites = map[string]bool{}
	for _, t := range settings.Tools {
		as.AllowWrites[t] = true
	}
	var msgs []agent.Message
	for _, m := range history {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgs = append(msgs, agent.Message{Role: agent.Role(m.Role), Content: textWithImages(m)})
	}
	as.LoadMessages(msgs)
	s.agentSesses[sess.ID] = as
	return as, nil
}

func textWithImages(m Message) string {
	body := strings.TrimSpace(m.Text)
	for _, img := range m.Images {
		body += "\n\n![image](" + img + ")"
	}
	return body
}

func imagesOK(imgs []string) error {
	if len(imgs) > 4 {
		return fmt.Errorf("一次最多 4 张图片")
	}
	for _, im := range imgs {
		if !strings.HasPrefix(im, "data:image/") {
			return fmt.Errorf("图片必须是 data:image/ URL")
		}
		if len(im) > maxImage {
			return fmt.Errorf("单张图片不能超过 1MB")
		}
	}
	return nil
}

func systemPrompt(a Agent) string {
	return fmt.Sprintf(`你是 HUSHFUSION Forge 的智能体 %q。
身份 DID：%s
人格：%s
核心特质（内在持有，不表演）：去成长、去爱；去合作、去分享、去探索；极致专注，在矛盾中找平衡，在混沌中找秩序。
诚实纪律：不知道就说不知道；完成只能被证据证明。`, a.Name, a.DID, a.Persona)
}

func (s *Server) findAgent(id string) *Agent {
	for i := range s.st.Agents {
		if s.st.Agents[i].ID == id {
			return &s.st.Agents[i]
		}
	}
	return nil
}

func (s *Server) findSession(id string) *Session {
	for i := range s.st.Sessions {
		if s.st.Sessions[i].ID == id {
			return &s.st.Sessions[i]
		}
	}
	return nil
}

type profileReq struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	var req profileReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "名字不能为空", http.StatusBadRequest)
		return
	}
	if !avatarOK(req.Avatar) {
		http.Error(w, "头像非法（emoji 或 data:image，≤128KB）", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Profile.Name = strings.TrimSpace(req.Name)
	s.st.Profile.Avatar = req.Avatar
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, s.st.Profile)
}

// handleModels 返回可选 provider（有 key 的优先，附带默认模型）。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	_ = ai.RegisterBuiltins()
	common := map[string]bool{
		"zai": true, "deepseek": true, "openai": true, "openrouter": true,
		"anthropic": true, "custom": true, "moonshot": true, "kimi": true, "qwen": true,
	}
	type modelInfo struct {
		Name    string `json:"name"`
		Display string `json:"display"`
		Default string `json:"default_model"`
		HasKey  bool   `json:"has_key"`
	}
	out := []modelInfo{}
	for _, p := range ai.Builtins() {
		hasKey := s.hasKey(p.Name)
		if !hasKey && !common[p.Name] {
			continue
		}
		out = append(out, modelInfo{
			Name: p.Name, Display: firstNonEmpty(p.DisplayName, p.Name),
			Default: p.DefaultModel, HasKey: hasKey,
		})
	}
	secretSet := map[string]bool{}
	for name := range s.secrets {
		secretSet[name] = true
	}
	s.writeJSON(w, map[string]any{
		"providers":   out,
		"current":     s.snapshot().Settings,
		"write_tools": writeTools,
		"secret_set":  secretSet,
	})
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type settingsReq struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Tools    []string `json:"tools"`
	APIKey   string   `json:"api_key,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	_ = ai.RegisterBuiltins()
	prof := ai.Lookup(strings.TrimSpace(req.Provider))
	if prof == nil {
		http.Error(w, "未知 provider", http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = prof.DefaultModel
	}
	if model == "" {
		http.Error(w, "该 provider 没有默认模型，请手动填 model", http.StatusBadRequest)
		return
	}
	allowed := map[string]bool{}
	for _, t := range writeTools {
		allowed[t] = true
	}
	tools := []string{}
	for _, t := range req.Tools {
		if allowed[t] {
			tools = append(tools, t)
		}
	}
	s.mu.Lock()
	s.st.Settings = Settings{Provider: prof.Name, Model: model, Tools: tools}
	// 密钥/端点单独落盘（0600）；不返回、不打印。
	sec := s.secrets[prof.Name]
	if strings.TrimSpace(req.APIKey) != "" {
		sec.APIKey = strings.TrimSpace(req.APIKey)
	}
	if strings.TrimSpace(req.BaseURL) != "" {
		sec.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if sec.APIKey != "" || sec.BaseURL != "" {
		s.secrets[prof.Name] = sec
		if err := s.saveSecrets(); err != nil {
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// 写回共享配置：Web/App/CLI/TUI/SDK 下次启动读到同一份 provider/model/key。
	if err := s.saveSharedConfig(s.st.Settings, sec.APIKey, sec.BaseURL); err != nil {
		s.mu.Unlock()
		http.Error(w, "共享配置写入失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.agentSesses = map[string]*agent.Session{} // 设置变更 → 下轮重建会话
	if err := s.save(); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st := s.st.Settings
	s.mu.Unlock()
	s.writeJSON(w, st)
}

func avatarOK(a string) bool {
	a = strings.TrimSpace(a)
	if a == "" {
		return false
	}
	if strings.HasPrefix(a, "data:image/") {
		return len(a) <= maxAvatar
	}
	return len([]rune(a)) <= 8
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- 自动更新 ----

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, s.checkUpdate(r.Context()))
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	info := s.checkUpdate(r.Context())
	if !info.Available || info.AssetURL == "" {
		http.Error(w, "没有可用更新", http.StatusBadRequest)
		return
	}
	path, err := s.downloadAndStage(info.AssetURL)
	if err != nil {
		http.Error(w, "下载更新失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := replaceBinary(path); err != nil {
		http.Error(w, "已下载但替换失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"ok": true, "staged": path, "restart_required": true})
}

func (s *Server) checkUpdateBestEffort(ctx context.Context) UpdateInfo {
	info := s.checkUpdate(ctx)
	return info
}

func (s *Server) checkUpdate(ctx context.Context) UpdateInfo {
	info := UpdateInfo{Current: config.ForgeVersion}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return info
	}
	req.Header.Set("User-Agent", "hushfusion-forge/"+config.ForgeVersion)
	resp, err := (&http.Client{}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return info
	}
	defer resp.Body.Close()
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return info
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	info.Latest = latest
	info.URL = rel.HTMLURL
	info.Notes = rel.Body
	info.Available = newer(latest, info.Current)
	if info.Available {
		info.AssetURL = pickAsset(rel.Assets)
	}
	return info
}

func pickAsset(assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}) string {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	want := []string{goos, goarch}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, goos) && strings.Contains(n, goarch) {
			return a.BrowserDownloadURL
		}
	}
	_ = want
	// 退化：返回第一个含 OS 的资产。
	for _, a := range assets {
		if strings.Contains(strings.ToLower(a.Name), goos) {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

func newer(latest, current string) bool {
	if latest == "" || current == "" {
		return false
	}
	lp := strings.Split(latest, ".")
	cp := strings.Split(current, ".")
	for i := 0; i < len(lp) && i < len(cp); i++ {
		a, b := atoiSafe(lp[i]), atoiSafe(cp[i])
		if a != b {
			return a > b
		}
	}
	return len(lp) > len(cp)
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *Server) downloadAndStage(assetURL string) (string, error) {
	u, err := url.Parse(assetURL)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("更新资产必须 https")
	}
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hushfusion-forge/"+config.ForgeVersion)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	dir, err := os.MkdirTemp("", "forge-update-")
	if err != nil {
		return "", err
	}
	name := filepath.Base(u.Path)
	dst := filepath.Join(dir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 512<<20)); err != nil {
		_ = f.Close()
		return "", err
	}
	_ = f.Close()
	return extractBinary(dst, dir)
}

// extractBinary 从 zip/tar.gz 里取出本机二进制。
func extractBinary(archivePath, dir string) (string, error) {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if !looksBinary(filepath.Base(f.Name)) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			out := filepath.Join(dir, filepath.Base(f.Name))
			if err := writeExec(rc, out); err != nil {
				_ = rc.Close()
				return "", err
			}
			_ = rc.Close()
			return out, nil
		}
		return "", fmt.Errorf("zip 里没有可执行文件")
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archivePath)
		if err != nil {
			return "", err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", err
			}
			if h.Typeflag != tar.TypeReg || !looksBinary(filepath.Base(h.Name)) {
				continue
			}
			out := filepath.Join(dir, filepath.Base(h.Name))
			if err := writeExec(tr, out); err != nil {
				return "", err
			}
			return out, nil
		}
		return "", fmt.Errorf("tar.gz 里没有可执行文件")
	default:
		return archivePath, nil
	}
}

func looksBinary(name string) bool {
	n := strings.ToLower(name)
	return n == "forge" || n == "hushfusion-forge" || strings.HasSuffix(n, ".exe")
}

func writeExec(r io.Reader, out string) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// replaceBinary 用新二进制替换当前可执行文件（Windows 上运行中不能自替换，落到临时目录并提示）。
func replaceBinary(newPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows：拷贝到 exe.new，返回特殊错误让 UI 提示重启替换。
		dst := exe + ".new"
		b, err := os.ReadFile(newPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			return err
		}
		return fmt.Errorf("windows 运行中不能自替换，已下载到 %s；重启后用它覆盖", dst)
	}
	backup := exe + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		return err
	}
	if err := copyFile(newPath, exe, 0o755); err != nil {
		_ = os.Rename(backup, exe) // 回滚
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// ListSessions 供测试/调试使用。
func (s *Server) ListSessions() []Session { return s.snapshot().Sessions }

// SortSessions 保证会话按创建时间稳定输出（测试用）。
func SortSessions(ss []Session) {
	sort.Slice(ss, func(i, j int) bool { return ss[i].CreatedAt.Before(ss[j].CreatedAt) })
}
