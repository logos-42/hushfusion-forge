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
	Role string    `json:"role"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// Session 是会话栏里的一条会话（归属某个智能体）。
type Session struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
}

// State 是落盘状态（~/.forge/web/state.json）。
type State struct {
	Profile         Profile   `json:"profile"`
	Agents          []Agent   `json:"agents"`
	Sessions        []Session `json:"sessions"`
	ActiveAgentID   string    `json:"active_agent_id"`
	ActiveSessionID string    `json:"active_session_id"`
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
	addr string
	dir  string
	mu   sync.Mutex
	st   State
	http *http.Server
}

const (
	stateFile = "state.json"
	maxAvatar = 128 * 1024
	repoSlug  = "logos-42/hushfusion-forge"
)

// New 构造服务器并加载/初始化状态。
func New(addr, dir string) (*Server, error) {
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
	s := &Server{addr: addr, dir: dir}
	if err := s.load(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/agents", s.handleCreateAgent)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/chat", s.handleChat)
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

func (s *Server) initState() error {
	id, err := identity.Generate()
	if err != nil {
		return err
	}
	now := time.Now()
	agent := Agent{
		ID:        "agent-1",
		Name:      "Forge 主智能体",
		Avatar:    "🧠",
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
	s.st = State{
		Profile:         Profile{Name: "Forge 用户", Avatar: "🙂", DID: id.DID()},
		Agents:          []Agent{agent},
		Sessions:        []Session{sess},
		ActiveAgentID:   agent.ID,
		ActiveSessionID: sess.ID,
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
	if !avatarOK(req.Avatar) {
		http.Error(w, "头像非法（emoji 或 data:image，≤128KB）", http.StatusBadRequest)
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
		Avatar:    req.Avatar,
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
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Message   string `json:"message"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求不是 JSON", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		http.Error(w, "消息为空", http.StatusBadRequest)
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
	sess.Messages = append(sess.Messages, Message{Role: "user", Text: msg, Time: time.Now()})
	if sess.Title == "新对话" && len([]rune(msg)) > 4 {
		sess.Title = string([]rune(msg)[:min(24, len([]rune(msg)))])
	}
	history := append([]Message(nil), sess.Messages...)
	if err := s.save(); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()

	reply, err := s.chat(r.Context(), *agent, history)
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

// chat 直接走 ai.Chat（v0.2.1 不挂工具，先把身份化主对话窗口跑通）。
func (s *Server) chat(ctx context.Context, agent Agent, history []Message) (string, error) {
	provider := envOr("HUSHFUSION_AI_PROVIDER", "zai")
	model := envOr("HUSHFUSION_AI_MODEL", "")
	baseURL := os.Getenv("HUSHFUSION_AI_BASE_URL")
	msgs := []ai.Message{{Role: "system", Content: systemPrompt(agent)}}
	for _, m := range history {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgs = append(msgs, ai.Message{Role: m.Role, Content: m.Text})
	}
	resp, err := ai.Chat(ctx, provider, ai.ChatRequest{Model: model, Messages: msgs}, ai.ClientOptions{BaseURL: baseURL})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
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
