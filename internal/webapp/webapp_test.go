package webapp

import (
	"encoding/json"
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
