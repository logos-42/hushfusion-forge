package webapp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	s, err := NewWithRoot("", t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(nil) })
	return httptest.NewServer(s.http.Handler)
}

func TestBootstrapHasIdentityAndAgents(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j struct {
		Version string `json:"version"`
		State   State  `json:"state"`
		Update  UpdateInfo
	}
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	if j.Version == "" || len(j.State.Agents) == 0 || j.State.Profile.DID == "" {
		t.Fatalf("bootstrap 不完整: %+v", j)
	}
	if !j.State.Settings.ToolsInitialized || len(j.State.Settings.Tools) != len(writeTools) {
		t.Fatalf("写工具应默认开启: %+v", j.State.Settings)
	}
}

func TestCreateAgentAndProfile(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/api/agents", "application/json",
		strings.NewReader(`{"name":"等离子体智能体","avatar":"🧪","persona":"聚焦等离子体控制"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建智能体 HTTP %d", resp.StatusCode)
	}
	resp2, err := http.Post(ts.URL+"/api/profile", "application/json",
		strings.NewReader(`{"name":"开发者","avatar":"🙂"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("保存头像 HTTP %d", resp2.StatusCode)
	}
	resp3, err := http.Post(ts.URL+"/api/profile", "application/json",
		strings.NewReader(`{"name":"x","avatar":"`+strings.Repeat("a", 200)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode == http.StatusOK {
		t.Fatal("超长头像应被拒绝")
	}
}

func TestUpdateAgentNameAndAvatar(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/api/agents", "application/json",
		strings.NewReader(`{"name":"旧名字","persona":"旧职责"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created struct {
		Agent Agent `json:"agent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + created.Agent.ID + `","name":"新名字","persona":"新职责","avatar":"data:image/png;base64,AAAA"}`
	resp2, err := http.Post(ts.URL+"/api/agents/update", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("更新智能体 HTTP %d", resp2.StatusCode)
	}
	var updated Agent
	if err := json.NewDecoder(resp2.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "新名字" || updated.Persona != "新职责" || updated.Avatar != "data:image/png;base64,AAAA" {
		t.Fatalf("更新结果 = %+v", updated)
	}
	if updated.DID == "" || updated.ID != created.Agent.ID {
		t.Fatal("更新不应改变 ID/DID")
	}
}

func TestAgentDirectories(t *testing.T) {
	root := t.TempDir()
	appDir := t.TempDir()
	s, err := NewWithRoot("", appDir, root)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.http.Handler)
	defer ts.Close()

	ws := t.TempDir()
	gd := filepath.Join(t.TempDir(), "agent-global")
	body, _ := json.Marshal(map[string]string{
		"name": "目录测试", "workspace": ws, "global_dir": gd,
	})
	resp, err := http.Post(ts.URL+"/api/agents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建智能体 HTTP %d", resp.StatusCode)
	}
	var created struct {
		Agent   Agent   `json:"agent"`
		Session Session `json:"session"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.Agent.Workspace != ws || created.Agent.GlobalDir != gd {
		t.Fatalf("智能体目录 = %+v", created.Agent)
	}
	if _, err := os.Stat(gd); err != nil {
		t.Fatalf("全局目录未创建: %v", err)
	}

	// 不传目录时：workspace 回落到项目根，全局目录回落到应用数据目录。
	body, _ = json.Marshal(map[string]string{"name": "默认目录"})
	resp, err = http.Post(ts.URL+"/api/agents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var fallback struct {
		Agent Agent `json:"agent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fallback); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if fallback.Agent.Workspace != root {
		t.Fatalf("默认 workspace = %q, want %q", fallback.Agent.Workspace, root)
	}
	wantGlobal := filepath.Join(appDir, "agents", fallback.Agent.ID)
	if fallback.Agent.GlobalDir != wantGlobal {
		t.Fatalf("默认 global = %q, want %q", fallback.Agent.GlobalDir, wantGlobal)
	}

	// 不存在的 workspace 必须拒绝，而不是悄悄回落到默认目录。
	resp, err = http.Post(ts.URL+"/api/agents/update", "application/json",
		bytes.NewReader([]byte(`{"id":"`+created.Agent.ID+`","name":"目录测试","workspace":"/definitely/not/here"}`)))
	if err == nil {
		resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 workspace 应 400, err=%v", err)
	}

	// Runtime 必须把记忆写到智能体的全局目录，而不是进程内存。
	as, err := s.agentSession(Settings{}, &Session{ID: "memory-test"}, created.Agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantMem := filepath.Join(gd, "memory.json")
	if as.Memory.Path() != wantMem {
		t.Fatalf("记忆文件 = %q, want %q", as.Memory.Path(), wantMem)
	}
	as.Memory.Set("soul_design", "ok")
	reloaded, err := agent.NewMemStoreFile(wantMem)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("soul_design"); got != "ok" {
		t.Fatalf("记忆未落盘: %q", got)
	}
}

func TestSessionActions(t *testing.T) {
	root := t.TempDir()
	s, err := NewWithRoot("", t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.http.Handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(`{"agent_id":"agent-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var sess Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp2, err := http.Post(ts.URL+"/api/sessions/update", "application/json",
		strings.NewReader(`{"id":"`+sess.ID+`","title":"重命名会话","pinned":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var updated Session
	if err := json.NewDecoder(resp2.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if updated.Title != "重命名会话" || !updated.Pinned {
		t.Fatalf("更新会话 = %+v", updated)
	}

	// 分支需要至少一条消息；直接补到状态里，避免真实模型调用。
	s.mu.Lock()
	for i := range s.st.Sessions {
		if s.st.Sessions[i].ID == sess.ID {
			s.st.Sessions[i].Messages = []Message{{Role: "user", Text: "分支起点", Time: time.Now()}}
		}
	}
	s.mu.Unlock()
	resp3, err := http.Post(ts.URL+"/api/sessions/branch", "application/json",
		strings.NewReader(`{"session_id":"`+sess.ID+`","message_index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	var branch Session
	if err := json.NewDecoder(resp3.Body).Decode(&branch); err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if branch.ID == sess.ID || len(branch.Messages) != 1 || branch.Messages[0].Text != "分支起点" {
		t.Fatalf("分支会话 = %+v", branch)
	}

	// 编辑消息后的截断：从第 0 条起删除，历史变空。
	respT, err := http.Post(ts.URL+"/api/sessions/truncate", "application/json",
		strings.NewReader(`{"id":"`+branch.ID+`","from_index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	var truncated Session
	if err := json.NewDecoder(respT.Body).Decode(&truncated); err != nil {
		t.Fatal(err)
	}
	respT.Body.Close()
	if len(truncated.Messages) != 0 {
		t.Fatalf("截断后消息 = %+v", truncated.Messages)
	}

	resp4, err := http.Post(ts.URL+"/api/sessions/share", "application/json",
		strings.NewReader(`{"id":"`+sess.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var share map[string]string
	if err := json.NewDecoder(resp4.Body).Decode(&share); err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if share["url"] == "" || share["token"] == "" {
		t.Fatalf("分享 = %+v", share)
	}
	page, err := http.Get(ts.URL + "/share/" + share["token"])
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(body), "重命名会话") {
		t.Fatalf("分享页 HTTP %d", page.StatusCode)
	}

	resp5, err := http.Post(ts.URL+"/api/sessions/delete", "application/json",
		strings.NewReader(`{"id":"`+sess.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusOK {
		t.Fatalf("删除会话 HTTP %d", resp5.StatusCode)
	}
}

func TestModelsAndSettings(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		Providers []struct {
			Name   string `json:"name"`
			HasKey bool   `json:"has_key"`
		} `json:"providers"`
		WriteTools []string `json:"write_tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if len(m.Providers) == 0 || len(m.WriteTools) == 0 {
		t.Fatalf("models = %+v", m)
	}
	resp2, err := http.Post(ts.URL+"/api/settings", "application/json",
		strings.NewReader(`{"provider":"deepseek","model":"deepseek-chat","tools":["mem.set","not-a-tool"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var st Settings
	if err := json.NewDecoder(resp2.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Provider != "deepseek" || st.Model != "deepseek-chat" || len(st.Tools) != 1 || st.Tools[0] != "mem.set" {
		t.Fatalf("settings = %+v", st)
	}
}

func TestImageValidation(t *testing.T) {
	if err := imagesOK([]string{"data:image/png;base64,AAAA"}); err != nil {
		t.Fatal(err)
	}
	if err := imagesOK([]string{"https://x/y.png"}); err == nil {
		t.Fatal("非 data:image 应拒绝")
	}
	if err := imagesOK([]string{"1", "2", "3", "4", "5"}); err == nil {
		t.Fatal("超过 4 张应拒绝")
	}
	big := "data:image/png;base64," + strings.Repeat("A", maxImage+10)
	if err := imagesOK([]string{big}); err == nil {
		t.Fatal("超过 1MB 应拒绝")
	}
	if got := textWithImages(Message{Text: "hi", Images: []string{"data:image/png;base64,AA"}}); !strings.Contains(got, "![image](data:image/png;base64,AA)") {
		t.Fatalf("textWithImages = %q", got)
	}
}

// Web 必须复用 <os>/forge.config.json（CLI/TUI/SDK 的同一份配置）。
func TestSharedWorkspaceConfig(t *testing.T) {
	root := t.TempDir()
	osDir := filepath.Join(root, "os")
	if err := os.MkdirAll(osDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"provider":"deepseek","model":"deepseek-reasoner","api_key":"sk-shared","base_url":"https://api.deepseek.com/v1"}`
	if err := os.WriteFile(filepath.Join(osDir, "forge.config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewWithRoot("", t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	st := s.snapshot()
	if st.Settings.Provider != "deepseek" || st.Settings.Model != "deepseek-reasoner" {
		t.Fatalf("未复用共享配置: %+v", st.Settings)
	}
	if sec := s.secretFor("deepseek"); sec.APIKey != "sk-shared" || sec.BaseURL == "" {
		t.Fatalf("未复用共享密钥/端点: %+v", sec)
	}
	ts := httptest.NewServer(s.http.Handler)
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/settings", "application/json",
		strings.NewReader(`{"provider":"custom","model":"m","api_key":"sk-new","base_url":"http://127.0.0.1:9/v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存 settings HTTP %d", resp.StatusCode)
	}
	b, err := os.ReadFile(filepath.Join(osDir, "forge.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["provider"] != "custom" || saved["model"] != "m" || saved["api_key"] != "sk-new" {
		t.Fatalf("共享配置未写回: %+v", saved)
	}
}

func TestSettingsSecretNotReturned(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/api/settings", "application/json",
		strings.NewReader(`{"provider":"custom","model":"m","api_key":"sk-should-not-return","base_url":"http://127.0.0.1:9/v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "sk-should-not-return") {
		t.Fatal("设置响应不应返回密钥明文")
	}
	m, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Body.Close()
	var j struct {
		SecretSet map[string]bool `json:"secret_set"`
	}
	if err := json.NewDecoder(m.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	if !j.SecretSet["custom"] {
		t.Fatalf("secret_set = %+v", j.SecretSet)
	}
}

func TestNewerVersion(t *testing.T) {
	if !newer("0.3.0", "0.2.1") {
		t.Fatal("0.3.0 应比 0.2.1 新")
	}
	if newer("0.2.1", "0.2.1") {
		t.Fatal("同版本不算更新")
	}
	if newer("0.2", "0.2.1") {
		t.Fatal("0.2 不应比 0.2.1 新")
	}
}

func TestAvatarValidation(t *testing.T) {
	if !avatarOK("🧠") {
		t.Fatal("emoji 头像应合法")
	}
	if avatarOK("") || avatarOK(strings.Repeat("x", 20)) {
		t.Fatal("空/超长字符串头像应非法")
	}
	if !avatarOK("data:image/png;base64,AAAA") {
		t.Fatal("data:image 头像应合法")
	}
}
