package webapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
