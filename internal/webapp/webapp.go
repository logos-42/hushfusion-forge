// Package webapp 是 HUSHFUSION Forge 的桌面/Web 外壳：本地 HTTP 服务 +
// 嵌入式前端。第一版聚焦“主对话窗口 + 侧边栏（智能体/会话/账号/自动更新）”，
// 不引入重型桌面框架；打包产物是本机二进制 + 内嵌 Web UI。
package webapp

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
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
	Workspace string    `json:"workspace,omitempty"`  // 工具根目录（read/ls/shell 的 cwd）
	GlobalDir string    `json:"global_dir,omitempty"` // 全局专属目录（记忆/跨项目资料）
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
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	Messages   []Message `json:"messages"`
	Pinned     bool      `json:"pinned,omitempty"`
	ShareToken string    `json:"share_token,omitempty"`
}

// Settings 是模型与工具设置（侧边栏/主窗口可选）。
type Settings struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	Tools            []string `json:"tools,omitempty"` // 需要放行的写工具；读工具默认可用
	ToolsInitialized bool     `json:"tools_initialized,omitempty"`
}

// State 是落盘状态（~/.forge/web/state.json）。
type State struct {
	Profile         Profile       `json:"profile"`
	Agents          []Agent       `json:"agents"`
	Sessions        []Session     `json:"sessions"`
	ActiveAgentID   string        `json:"active_agent_id"`
	ActiveSessionID string        `json:"active_session_id"`
	Settings        Settings      `json:"settings"`
	Room            []RoomMessage `json:"room,omitempty"`
}

// RoomMessage 是智能体内部商议频道的一条消息（仅 Web 内部使用）。
type RoomMessage struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	Kind      string    `json:"kind"` // report / discuss / invite
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
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

	// runMu/runs 管理服务端后台对话任务：任务不绑浏览器请求生命周期。
	runMu sync.Mutex
	runs  map[string]*chatRun

	// workspaceLocks 按 workspace 串行化 shell，避免并发会话互相踩目录。
	workspaceMu    sync.Mutex
	workspaceLocks map[string]*sync.Mutex

	kernel *dualKernel
}

// chatRun 是一轮后台对话的共享状态（部分文本 + 取消）。
type chatRun struct {
	cancel  context.CancelFunc
	started time.Time
	lane    *kernelLane

	mu   sync.Mutex
	text string
}

func (r *chatRun) append(s string) {
	r.mu.Lock()
	r.text += s
	r.mu.Unlock()
}

func (r *chatRun) snapshot() (string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text, r.started
}

func (s *Server) beginRun(sessionID string) (*chatRun, context.Context, bool) {
	lane, ok := s.kernel.acquire()
	if !ok {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &chatRun{cancel: cancel, started: time.Now(), lane: lane}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if _, exists := s.runs[sessionID]; exists {
		cancel()
		s.kernel.release(lane)
		return nil, nil, false
	}
	s.runs[sessionID] = run
	return run, ctx, true
}

func (s *Server) endRun(sessionID string, run *chatRun) {
	run.cancel()
	s.runMu.Lock()
	if s.runs[sessionID] == run {
		delete(s.runs, sessionID)
	}
	s.runMu.Unlock()
	s.kernel.release(run.lane)
}

func (s *Server) currentRun(sessionID string) *chatRun {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runs[sessionID]
}

func (s *Server) handleChatStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("session_id"))
	run := s.currentRun(id)
	if run == nil {
		s.writeJSON(w, map[string]any{"running": false})
		return
	}
	text, started := run.snapshot()
	s.writeJSON(w, map[string]any{
		"running": true, "text": text, "started_at": started,
	})
}

func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	run := s.currentRun(strings.TrimSpace(req.SessionID))
	if run == nil {
		s.writeJSON(w, map[string]any{"stopped": false})
		return
	}
	run.cancel()
	s.writeJSON(w, map[string]any{"stopped": true})
}

func (s *Server) handleKernel(w http.ResponseWriter, r *http.Request) {
	s.runMu.Lock()
	runs := make([]KernelRunInfo, 0, len(s.runs))
	for id, run := range s.runs {
		text, _ := run.snapshot()
		lane := ""
		if run.lane != nil {
			lane = run.lane.role
		}
		runs = append(runs, KernelRunInfo{SessionID: id, Lane: lane, StartedAt: run.started.UnixMilli(), TextLen: len([]rune(text))})
	}
	s.runMu.Unlock()
	s.writeJSON(w, s.kernel.snapshot(runs))
}

func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	msgs := make([]RoomMessage, 0, len(s.st.Room))
	msgs = append(msgs, s.st.Room...)
	s.mu.Unlock()
	s.writeJSON(w, map[string]any{"messages": msgs})
}

// handleRoomPost 仅供本地 UI 调试/邀请；智能体走 room_post 工具。
func (s *Server) handleRoomPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		http.Error(w, "消息不能为空", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ag := s.findAgent(strings.TrimSpace(req.AgentID))
	sess := s.findSession(strings.TrimSpace(req.SessionID))
	var a Agent
	var title string
	if ag != nil {
		a = *ag
	}
	if sess != nil {
		title = sess.Title
	}
	s.mu.Unlock()
	s.postRoom(a, title, req.SessionID, "discuss", text)
	s.writeJSON(w, map[string]any{"ok": true})
}

// postRoom 追加一条内部商议消息；最多保留最近 300 条。
func (s *Server) postRoom(ag Agent, sessionTitle, sessionID, kind, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len([]rune(text)) > 4000 {
		text = string([]rune(text)[:4000]) + "\n…(截断)"
	}
	msg := RoomMessage{
		ID:        fmt.Sprintf("room-%d", time.Now().UnixNano()),
		AgentID:   ag.ID,
		AgentName: ag.Name,
		SessionID: sessionID,
		Title:     sessionTitle,
		Kind:      kind,
		Text:      text,
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.st.Room = append(s.st.Room, msg)
	if len(s.st.Room) > 300 {
		s.st.Room = s.st.Room[len(s.st.Room)-300:]
	}
	_ = s.save()
	s.mu.Unlock()
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
		agentSesses: map[string]*agent.Session{}, secrets: map[string]Secret{},
		runs: map[string]*chatRun{}, workspaceLocks: map[string]*sync.Mutex{},
		kernel: newDualKernel(dir, 4)}
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
	mux.HandleFunc("POST /api/agents/update", s.handleUpdateAgent)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/sessions/update", s.handleUpdateSession)
	mux.HandleFunc("POST /api/sessions/delete", s.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/branch", s.handleBranchSession)
	mux.HandleFunc("POST /api/sessions/truncate", s.handleTruncateSession)
	mux.HandleFunc("POST /api/sessions/share", s.handleShareSession)
	mux.HandleFunc("GET /share/{token}", s.handleSharePage)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat/stream", s.handleChatStream)
	mux.HandleFunc("GET /api/chat/status", s.handleChatStatus)
	mux.HandleFunc("POST /api/chat/stop", s.handleChatStop)
	mux.HandleFunc("GET /api/room", s.handleRoom)
	mux.HandleFunc("POST /api/room", s.handleRoomPost)
	mux.HandleFunc("GET /api/kernel", s.handleKernel)
	mux.HandleFunc("POST /api/profile", s.handleProfile)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("POST /api/update/apply", s.handleUpdateApply)
	mux.Handle("/", http.FileServer(http.FS(assets)))
	s.http = &http.Server{Addr: addr, Handler: security(mux)}
	return s, nil
}

// Addr 返回监听地址。
func (s *Server) Addr() string { return s.addr }

// ListenAndServe 启动服务（阻塞）。
func (s *Server) ListenAndServe() error {
	s.kernel.startHeartbeat()
	// 自更新时新进程可能比旧进程早几百毫秒起来；端口被占时短暂重试，
	// 避免出现“新二进制已替换，但新进程 bind 失败退出”。
	var ln net.Listener
	var err error
	for i := 0; i < 40; i++ {
		ln, err = net.Listen("tcp", s.addr)
		if err == nil {
			break
		}
		if i == 39 {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return s.http.Serve(ln)
}

// Shutdown 优雅停止。
func (s *Server) Shutdown(ctx context.Context) error {
	s.kernel.close()
	return s.http.Shutdown(ctx)
}

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
func needsNewAvatar(a string) bool {
	if !strings.HasPrefix(a, "data:image/") {
		return true
	}
	// 旧版生成头像含 <text>（URL 编码后是 %3Ctext）；统一迁移到无文字抽象头像。
	return strings.HasPrefix(a, "data:image/svg+xml") &&
		(strings.Contains(a, "<text") || strings.Contains(a, "%3Ctext") || strings.Contains(a, "+text"))
}

func (s *Server) normalize() {
	if needsNewAvatar(s.st.Profile.Avatar) {
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
	// 写工具默认全开；用户显式保存过空列表则尊重用户选择。
	if !s.st.Settings.ToolsInitialized {
		s.st.Settings.Tools = append([]string(nil), writeTools...)
		s.st.Settings.ToolsInitialized = true
	}
	for i := range s.st.Agents {
		if needsNewAvatar(s.st.Agents[i].Avatar) {
			s.st.Agents[i].Avatar = svgAvatar(s.st.Agents[i].ID, firstRune(s.st.Agents[i].Name))
		}
		if strings.TrimSpace(s.st.Agents[i].Workspace) == "" {
			s.st.Agents[i].Workspace = s.root
		}
		if strings.TrimSpace(s.st.Agents[i].GlobalDir) == "" {
			s.st.Agents[i].GlobalDir = s.globalDirFor(s.st.Agents[i].ID)
		}
		_ = os.MkdirAll(s.st.Agents[i].GlobalDir, 0o700)
	}
	_ = s.save()
}

func (s *Server) workspaceLock(root string) *sync.Mutex {
	key, err := filepath.Abs(root)
	if err != nil {
		key = root
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if l := s.workspaceLocks[key]; l != nil {
		return l
	}
	l := &sync.Mutex{}
	s.workspaceLocks[key] = l
	return l
}

// globalDirFor 返回智能体在用户级 Forge 数据目录下的专属目录。
func (s *Server) globalDirFor(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		agentID = "default"
	}
	return filepath.Join(s.dir, "agents", filepath.Base(agentID))
}

// resolveAgentDir 把用户输入解析成绝对目录；空值用 fallback，支持 ~。
func resolveAgentDir(input, fallback string) (string, error) {
	p := strings.TrimSpace(input)
	if p == "" {
		p = strings.TrimSpace(fallback)
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func requireWorkspaceDir(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("workspace 不存在：%w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("workspace 不是目录：%s", path)
	}
	return nil
}

func firstRune(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "F"
	}
	return string(r[0])
}

// svgAvatar 生成一个好看的渐变 SVG 头像（data:image/svg+xml），不用 emoji。
// svgAvatar 生成不带文字的抽象头像（渐变球 + 光环 + 眼睛），避免字母/emoji 头像。
func svgAvatar(seed, _ string) string {
	h := 0
	for _, r := range seed {
		h = (h*31 + int(r)) % 360
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96" viewBox="0 0 96 96" data-forge-avatar="1"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(%d,88%%,66%%)"/><stop offset="1" stop-color="hsl(%d,72%%,38%%)"/></linearGradient><radialGradient id="h" cx="30%%" cy="22%%" r="70%%"><stop offset="0" stop-color="rgba(255,255,255,.55)"/><stop offset="1" stop-color="rgba(255,255,255,0)"/></radialGradient></defs><rect width="96" height="96" rx="48" fill="url(#g)"/><circle cx="48" cy="48" r="46" fill="url(#h)"/><circle cx="70" cy="28" r="12" fill="rgba(255,255,255,.20)"/><path d="M22 68 Q48 42 74 68" stroke="rgba(255,255,255,.40)" stroke-width="5" fill="none" stroke-linecap="round"/><circle cx="36" cy="45" r="5" fill="#fff" opacity=".92"/><circle cx="60" cy="45" r="5" fill="#fff" opacity=".92"/></svg>`,
		h, (h+42)%360)
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
		Settings: Settings{
			Provider: provider, Model: model,
			Tools: append([]string(nil), writeTools...), ToolsInitialized: true,
		},
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
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
	Persona   string `json:"persona"`
	Workspace string `json:"workspace"`
	GlobalDir string `json:"global_dir"`
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
	workspace, err := resolveAgentDir(req.Workspace, s.root)
	if err != nil {
		http.Error(w, "workspace 路径非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := requireWorkspaceDir(workspace); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := identity.Generate()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())
	globalDir, err := resolveAgentDir(req.GlobalDir, s.globalDirFor(agentID))
	if err != nil {
		http.Error(w, "全局目录路径非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		http.Error(w, "创建全局目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	agent := Agent{
		ID:        agentID,
		Name:      name,
		Avatar:    avatar,
		DID:       id.DID(),
		Persona:   strings.TrimSpace(req.Persona),
		Workspace: workspace,
		GlobalDir: globalDir,
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

type updateAgentReq struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
	Persona   string `json:"persona"`
	Workspace string `json:"workspace"`
	GlobalDir string `json:"global_dir"`
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	var req updateAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "智能体名字不能为空", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ag := s.findAgent(strings.TrimSpace(req.ID))
	if ag == nil {
		http.Error(w, "智能体不存在", http.StatusNotFound)
		return
	}
	avatar := strings.TrimSpace(req.Avatar)
	if avatar == "" {
		avatar = ag.Avatar
	}
	if !avatarOK(avatar) {
		http.Error(w, "头像非法（data:image，≤128KB）", http.StatusBadRequest)
		return
	}
	workspace, err := resolveAgentDir(req.Workspace, s.root)
	if err != nil {
		http.Error(w, "workspace 路径非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := requireWorkspaceDir(workspace); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	globalDir, err := resolveAgentDir(req.GlobalDir, s.globalDirFor(ag.ID))
	if err != nil {
		http.Error(w, "全局目录路径非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		http.Error(w, "创建全局目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ag.Name = name
	ag.Avatar = avatar
	ag.Persona = strings.TrimSpace(req.Persona)
	ag.Workspace = workspace
	ag.GlobalDir = globalDir
	s.agentSesses = map[string]*agent.Session{} // 人格/目录变更 → 下轮重建系统提示
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, *ag)
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

// handleUpdateSession 重命名 / 固定会话。
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Pinned *bool  `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.findSession(strings.TrimSpace(req.ID))
	if sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if t := strings.TrimSpace(req.Title); t != "" {
		sess.Title = t
	}
	if req.Pinned != nil {
		sess.Pinned = *req.Pinned
	}
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, *sess)
}

// handleDeleteSession 删除会话；若删的是当前会话，自动切到同智能体下一个会话。
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(req.ID)
	idx := -1
	for i := range s.st.Sessions {
		if s.st.Sessions[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	s.st.Sessions = append(s.st.Sessions[:idx], s.st.Sessions[idx+1:]...)
	delete(s.agentSesses, id)
	var created *Session
	if s.st.ActiveSessionID == id {
		s.st.ActiveSessionID = ""
		for i := range s.st.Sessions {
			if s.st.Sessions[i].AgentID == s.st.ActiveAgentID {
				s.st.ActiveSessionID = s.st.Sessions[i].ID
				break
			}
		}
		if s.st.ActiveSessionID == "" {
			sess := Session{ID: fmt.Sprintf("sess-%d", time.Now().UnixNano()), AgentID: s.st.ActiveAgentID, Title: "新对话", CreatedAt: time.Now()}
			s.st.Sessions = append(s.st.Sessions, sess)
			s.st.ActiveSessionID = sess.ID
			created = &sess
		}
	}
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"deleted": id, "active_session_id": s.st.ActiveSessionID, "session": created})
}

// handleBranchSession 从某条消息处分叉出一个新会话（复制到该消息为止）。
func (s *Server) handleBranchSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID    string `json:"session_id"`
		MessageIndex int    `json:"message_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.findSession(strings.TrimSpace(req.SessionID))
	if src == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if req.MessageIndex < 0 || req.MessageIndex >= len(src.Messages) {
		http.Error(w, "消息位置非法", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(src.Title)
	if title == "" {
		title = "新对话"
	}
	sess := Session{
		ID:        fmt.Sprintf("sess-%d", time.Now().UnixNano()),
		AgentID:   src.AgentID,
		Title:     title + " · 分支",
		CreatedAt: time.Now(),
		Messages:  append([]Message(nil), src.Messages[:req.MessageIndex+1]...),
	}
	s.st.Sessions = append(s.st.Sessions, sess)
	s.st.ActiveAgentID = sess.AgentID
	s.st.ActiveSessionID = sess.ID
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, sess)
}

// handleTruncateSession 从某条消息起截断，用于编辑已发送消息后重发。
func (s *Server) handleTruncateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		FromIndex int    `json:"from_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.findSession(strings.TrimSpace(req.ID))
	if sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if req.FromIndex < 0 || req.FromIndex > len(sess.Messages) {
		http.Error(w, "消息位置非法", http.StatusBadRequest)
		return
	}
	sess.Messages = sess.Messages[:req.FromIndex]
	delete(s.agentSesses, sess.ID) // 丢弃旧上下文，下轮按截断后的历史重建
	if err := s.save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, sess)
}

// handleShareSession 为会话生成只读分享 token/链接。
func (s *Server) handleShareSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.findSession(strings.TrimSpace(req.ID))
	if sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if sess.ShareToken == "" {
		sess.ShareToken = newShareToken()
		if err := s.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); v != "" {
		scheme = v
	}
	url := scheme + "://" + r.Host + "/share/" + sess.ShareToken
	s.writeJSON(w, map[string]string{"url": url, "token": sess.ShareToken})
}

func newShareToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	s.mu.Lock()
	var sess *Session
	for i := range s.st.Sessions {
		if s.st.Sessions[i].ShareToken == token && token != "" {
			sess = &s.st.Sessions[i]
			break
		}
	}
	if sess == nil {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	title := sess.Title
	agentName := sess.AgentID
	if ag := s.findAgent(sess.AgentID); ag != nil {
		agentName = ag.Name
	}
	msgs := append([]Message(nil), sess.Messages...)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>
:root{color-scheme:light dark;--bg:#000;--panel:#111418;--line:#252b34;--text:#f2f6fa;--muted:#9aa6b2}
@media(prefers-color-scheme:light){:root{--bg:#f5f7fa;--panel:#fff;--line:#dfe5ec;--text:#11151a;--muted:#68727d}}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC",sans-serif}
main{max-width:820px;margin:0 auto;padding:28px 18px}
h1{font-size:20px;margin:0 0 4px}.meta{color:var(--muted);margin-bottom:20px}
.msg{padding:10px 12px;border-radius:12px;margin:10px 0;white-space:pre-wrap;max-width:82%%}
.user{margin-left:auto;background:#1e3a5f}.bot{background:var(--panel);border:1px solid var(--line)}
.role{font-size:11px;color:var(--muted);margin-bottom:3px}
</style></head><body><main><h1>%s</h1><div class="meta">%s · HUSHFUSION Forge 只读分享</div>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(agentName))
	for _, m := range msgs {
		role := "bot"
		label := agentName
		if m.Role == "user" {
			role, label = "user", "用户"
		}
		_, _ = fmt.Fprintf(w, `<div class="msg %s"><div class="role">%s</div>%s</div>`, role, html.EscapeString(label), html.EscapeString(m.Text))
	}
	_, _ = io.WriteString(w, `</main></body></html>`)
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
	run, ctx, ok := s.beginRun(sess.ID)
	if !ok {
		s.mu.Unlock()
		http.Error(w, "上一轮还在后台运行，请等待完成或先停止", http.StatusConflict)
		return
	}
	defer s.endRun(sess.ID, run)
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
	reply, err := as.Turn(ctx, textWithImages(Message{Role: "user", Text: msg, Images: req.Images}))
	if err != nil {
		s.writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Text: reply, Time: time.Now()})
	title := sess.Title
	_ = s.save()
	s.mu.Unlock()
	s.postRoom(*agent, title, sess.ID, "report", reply)
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
	run, ctx, ok := s.beginRun(sess.ID)
	if !ok {
		s.mu.Unlock()
		http.Error(w, "上一轮还在后台运行，请等待完成或先停止", http.StatusConflict)
		return
	}
	defer s.endRun(sess.ID, run)
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
	var writeMu sync.Mutex
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		writeMu.Lock()
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
		writeMu.Unlock()
	}
	// 心跳：任务长时间无 token 时也保持 SSE 连接；浏览器断开不影响任务。
	heartbeatDone := make(chan struct{})
	heartbeatExited := make(chan struct{})
	go func() {
		defer close(heartbeatExited)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				writeMu.Lock()
				_, _ = fmt.Fprint(w, ": ping\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				writeMu.Unlock()
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer func() {
		close(heartbeatDone)
		<-heartbeatExited
	}()

	as, err := s.agentSession(settings, sess, *ag, history)
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	reply, err := as.TurnStream(ctx, textWithImages(Message{Role: "user", Text: msg, Images: req.Images}), func(ev ai.StreamEvent) {
		if ev.Type == ai.StreamContent && ev.Text != "" {
			run.append(ev.Text)
			send("delta", map[string]string{"text": ev.Text})
		}
	})
	if err != nil {
		partial, _ := run.snapshot()
		if strings.TrimSpace(partial) == "" {
			partial = "（错误）" + err.Error()
		} else {
			partial += "\n\n（已中断：" + err.Error() + "）"
		}
		s.mu.Lock()
		sess.Messages = append(sess.Messages, Message{Role: "assistant", Text: partial, Time: time.Now()})
		title := sess.Title
		_ = s.save()
		s.mu.Unlock()
		s.postRoom(*ag, title, sess.ID, "report", "任务中断："+err.Error())
		send("error", map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Text: reply, Time: time.Now()})
	title := sess.Title
	_ = s.save()
	s.mu.Unlock()
	s.postRoom(*ag, title, sess.ID, "report", reply)
	send("done", map[string]string{"reply": reply})
}

// agentSession 为 Web 会话构造/复用 agent Runtime（带工具；写工具按设置放行）。
// history 是「本轮 user 之前」的历史，避免 Turn/TurnStream 再追加时重复。
// registerRoomTools 给智能体内部商议频道入口；自动汇报由任务结束时投递。
func (s *Server) registerRoomTools(as *agent.Session, sess *Session, ag Agent) error {
	readSpec := ai.ToolSpec{Type: "function"}
	readSpec.Function.Name = "room.read"
	readSpec.Function.Description = "读取智能体内部商议频道最近的消息（只读，用于了解其它智能体的汇报/讨论）"
	if err := as.RegisterTool(agent.Tool{Spec: readSpec, Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
		s.mu.Lock()
		msgs := append([]RoomMessage(nil), s.st.Room...)
		s.mu.Unlock()
		if len(msgs) == 0 {
			return "（内部商议暂无消息）", nil
		}
		if len(msgs) > 80 {
			msgs = msgs[len(msgs)-80:]
		}
		var b strings.Builder
		for _, m := range msgs {
			who := m.AgentName
			if who == "" {
				who = m.AgentID
			}
			fmt.Fprintf(&b, "[%s] %s · %s\n%s\n\n", m.Kind, who, m.CreatedAt.Format("15:04:05"), m.Text)
		}
		return b.String(), nil
	}}); err != nil {
		return err
	}
	postSpec := ai.ToolSpec{Type: "function"}
	postSpec.Function.Name = "room.post"
	postSpec.Function.Description = "向智能体内部商议频道发一条消息（仅内部使用；不要用它代替给用户的最终回复）"
	postSpec.Function.Parameters = map[string]any{
		"type": "object", "properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "要发给其它智能体的消息"},
		}, "required": []string{"text"},
	}
	return as.RegisterTool(agent.Tool{Spec: postSpec, Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
		text, _ := args["text"].(string)
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("room.post: text 为空")
		}
		s.postRoom(ag, sess.Title, sess.ID, "discuss", text)
		return "已发送到内部商议频道", nil
	}})
}

func (s *Server) agentSession(settings Settings, sess *Session, ag Agent, history []Message) (*agent.Session, error) {
	if s.agentSesses == nil {
		s.agentSesses = map[string]*agent.Session{}
	}
	if as, ok := s.agentSesses[sess.ID]; ok {
		return as, nil
	}
	root := strings.TrimSpace(ag.Workspace)
	if root == "" {
		root = strings.TrimSpace(s.root)
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	globalDir := strings.TrimSpace(ag.GlobalDir)
	if globalDir == "" {
		globalDir = s.globalDirFor(ag.ID)
	}
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建全局目录失败: %w", err)
	}
	cfg := agent.DefaultConfig()
	cfg.Provider, cfg.Model = settings.Provider, settings.Model
	cfg.MaxBudgetTokens = agent.BudgetFromEnv(agent.BudgetForModel(settings.Model, 0.6))
	// Web 任务在后台运行，单次模型调用给更长窗口；仍保留上限避免挂死。
	cfg.ModelTimeout = 10 * time.Minute
	cfg.ShellTimeout = 10 * time.Minute
	cfg.Root = root
	cfg.GlobalRoot = globalDir
	cfg.WorkspaceLock = s.workspaceLock(root)
	opts := func() ai.ClientOptions {
		sec := s.secretFor(settings.Provider)
		base := firstNonEmpty(sec.BaseURL, os.Getenv("HUSHFUSION_AI_BASE_URL"))
		return ai.ClientOptions{BaseURL: base, APIKey: sec.APIKey}
	}
	chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		return ai.Chat(ctx, settings.Provider, req, opts())
	}
	as := agent.NewSession(cfg, chat, agent.NewRegistry(), systemPrompt(ag))
	mem, err := agent.NewMemStoreFile(filepath.Join(globalDir, "memory.json"))
	if err != nil {
		return nil, fmt.Errorf("加载智能体记忆失败: %w", err)
	}
	as.Memory = mem
	if mem.Len() > 0 {
		as.AppendSystem(mem.Summary())
	}
	as.SetStreamChat(func(ctx context.Context, req ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error) {
		return ai.ChatStream(ctx, settings.Provider, req, on, opts())
	})
	_ = as.RegisterBuiltinTools(nil) // read/ls/shell/mem/tasks；skill.view 无技能目录不注册
	_ = s.registerRoomTools(as, sess, ag)
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
	memFile := ""
	if strings.TrimSpace(a.GlobalDir) != "" {
		memFile = filepath.Join(a.GlobalDir, "memory.json")
	}
	return fmt.Sprintf(`你是 HUSHFUSION Forge 的智能体 %q。
身份 DID：%s
人格：%s
Workspace：%s（read/ls 的相对路径从这里解析；shell 的 cwd）
全局专属目录：%s（跨项目的记忆/资料；read/ls 允许访问这里的绝对路径）
记忆文件：%s（mem.set 会写入这里；跨会话保留）
路径规则：以上路径由系统自动注入，不需要问用户；不确定时调用 agent_paths 自查。不要声称存在未经验证的文件。
内部商议：任务结束后系统会自动把结果投递到内部商议频道；需要与其它智能体讨论时用 room_read 读取、room_post 发言。内部商议不能替代给用户的最终回复。
核心特质（内在持有，不表演）：去成长、去爱；去合作、去分享、去探索；极致专注，在矛盾中找平衡，在混沌中找秩序。
诚实纪律：不知道就说不知道；完成只能被证据证明。`, a.Name, a.DID, a.Persona, a.Workspace, a.GlobalDir, memFile)
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
	s.st.Settings = Settings{Provider: prof.Name, Model: model, Tools: tools, ToolsInitialized: true}
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

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]string{"version": config.ForgeVersion})
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
	// 先把新进程拉起来（它带等待时间，避免和当前进程抢端口），
	// 再回响应并退出；前端轮询到新版本后自动刷新。
	if err := startReplacement(); err != nil {
		http.Error(w, "已替换但重启失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"ok": true, "restarting": true, "version": info.Latest})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		os.Exit(0)
	}()
}

func (s *Server) checkUpdateBestEffort(ctx context.Context) UpdateInfo {
	info := s.checkUpdate(ctx)
	return info
}

func (s *Server) checkUpdate(ctx context.Context) UpdateInfo {
	info := UpdateInfo{Current: config.ForgeVersion}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// npm registry 优先：无 GitHub 未认证 API 的 60 次/小时限流。
	if next, ok := s.checkUpdateViaNPM(ctx, info); ok {
		return next
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return info
	}
	req.Header.Set("User-Agent", "hushfusion-forge/"+config.ForgeVersion)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return info
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		// 未认证 GitHub API 只有 60 次/小时；被限流时退回 releases/latest
		// 的 302 跳转，不消耗 API 配额，也不影响更新按钮。
		return s.checkUpdateViaLatest(ctx, info)
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
		return s.checkUpdateViaLatest(ctx, info)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	info.Latest = latest
	info.URL = rel.HTMLURL
	info.Notes = rel.Body
	info.Available = newer(latest, info.Current)
	if info.Available {
		info.AssetURL = pickAsset(rel.Assets)
	}
	if info.Latest == "" {
		return s.checkUpdateViaLatest(ctx, info)
	}
	return info
}

// checkUpdateViaNPM 从 npm registry 读 @hibs/forge 的最新版本。
// 返回 ok=false 表示 npm 不可用/无版本号，调用方继续走 GitHub 兜底。
func (s *Server) checkUpdateViaNPM(ctx context.Context, info UpdateInfo) (UpdateInfo, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://registry.npmjs.org/@hibs%2fforge/latest", nil)
	if err != nil {
		return info, false
	}
	req.Header.Set("User-Agent", "hushfusion-forge/"+config.ForgeVersion)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return info, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, false
	}
	var rel struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return info, false
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.Version), "v")
	if latest == "" {
		return info, false
	}
	info.Latest = latest
	info.URL = "https://github.com/" + repoSlug + "/releases/tag/v" + latest
	info.Available = newer(latest, info.Current)
	if info.Available {
		info.AssetURL = releaseAssetURL(latest)
	}
	return info, true
}

// checkUpdateViaLatest 用 releases/latest 的跳转获取最新 tag，作为 API
// 被限流/不可用时的兜底；资产 URL 按 Release 命名规则构造。
func (s *Server) checkUpdateViaLatest(ctx context.Context, info UpdateInfo) UpdateInfo {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://github.com/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return info
	}
	req.Header.Set("User-Agent", "hushfusion-forge/"+config.ForgeVersion)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return info
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
		return info
	}
	tag := loc[strings.LastIndex(loc, "/")+1:]
	latest := strings.TrimPrefix(tag, "v")
	if latest == "" {
		return info
	}
	info.Latest = latest
	info.URL = loc
	info.Available = newer(latest, info.Current)
	if info.Available {
		info.AssetURL = releaseAssetURL(latest)
	}
	return info
}

func releaseAssetURL(version string) string {
	name := fmt.Sprintf("hushfusion-forge-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repoSlug, version, name)
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

// replaceBinary 用新二进制替换当前可执行文件。
// Unix 和 Windows 都先把自己改名到 .bak，再把新文件放到原路径：
// 运行中的进程继续从 .bak 执行，新进程从原路径启动。
func replaceBinary(newPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
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
