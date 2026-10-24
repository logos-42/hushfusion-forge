package webapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := New("", t.TempDir())
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
