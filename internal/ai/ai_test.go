// ai 包的测试：全部用 httptest 假端点，零真实网络。
// 覆盖：注册表/别名、请求形状（URL/method/header/body）、响应解析、
// 非 2xx fail-closed、anthropic 模式未实现报错、key 解析优先级。
package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// 每个测试都从内置目录起跑（幂等注册）。
	if err := RegisterBuiltins(); err != nil {
		panic(err)
	}
	m.Run()
}

// fakeOpenAI 起一个最小 chat/completions 假端点，记录收到的请求（含 body 正文）
// 供断言用。body 在 handler 里读好（请求体在 handler 返回后会被关闭）。
func fakeOpenAI(t *testing.T) (*httptest.Server, **http.Request, *string) {
	t.Helper()
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"fake-1","model":"fake-model",` +
			`"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &body
}

func TestChatRequestShape(t *testing.T) {
	srv, got, body := fakeOpenAI(t)
	c, err := NewClient(&Profile{Name: "test", BaseURL: srv.URL, EnvVars: []string{"AI_TEST_KEY"}}, ClientOptions{APIKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m1",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *got == nil {
		t.Fatal("请求没到达假端点")
	}
	if (*got).Method != http.MethodPost {
		t.Errorf("method = %s, want POST", (*got).Method)
	}
	if !strings.HasSuffix((*got).URL.Path, "/chat/completions") {
		t.Errorf("path = %s, want .../chat/completions", (*got).URL.Path)
	}
	if (*got).Header.Get("Authorization") != "Bearer k1" {
		t.Errorf("Authorization = %q, want Bearer k1", (*got).Header.Get("Authorization"))
	}
	var req ChatRequest
	if err := json.Unmarshal([]byte(*body), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "m1" || len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
		t.Errorf("body 解析不符: %+v", req)
	}
	if resp.Content != "pong" || resp.FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestDefaultModelUsedWhenEmpty(t *testing.T) {
	srv, _, body := fakeOpenAI(t)
	c, err := NewClient(&Profile{Name: "test", BaseURL: srv.URL, DefaultModel: "def-model"}, ClientOptions{APIKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	var req ChatRequest
	json.Unmarshal([]byte(*body), &req)
	if req.Model != "def-model" {
		t.Errorf("model = %q, want def-model", req.Model)
	}
}

func TestChatHTTPErrorFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)
	c, _ := NewClient(&Profile{Name: "test", BaseURL: srv.URL}, ClientOptions{APIKey: "k1"})
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误消息应含状态码: %v", err)
	}
}

func TestAnthropicModeNotImplemented(t *testing.T) {
	_, err := NewClient(&Profile{Name: "anthropic", BaseURL: "https://api.anthropic.com", APIMode: ModeAnthropicMessages}, ClientOptions{APIKey: "k1"})
	if err == nil {
		t.Fatal("anthropic_messages 模式必须 fail-closed")
	}
	if !strings.Contains(err.Error(), "未实现") {
		t.Errorf("错误消息应说明未实现: %v", err)
	}
}

func TestStreamNotImplemented(t *testing.T) {
	srv, _, _ := fakeOpenAI(t)
	c, _ := NewClient(&Profile{Name: "test", BaseURL: srv.URL}, ClientOptions{APIKey: "k1"})
	_, err := c.Chat(context.Background(), ChatRequest{Stream: true, Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("stream=true 必须报错")
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	t.Cleanup(srv.Close)
	c, _ := NewClient(&Profile{Name: "test", BaseURL: srv.URL}, ClientOptions{APIKey: "k1"})
	ids, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ids = %v", ids)
	}
}

// AllowNoKey：无 key 也能探测 /models（本地/自托管），且不发送空 Bearer；
// 未设 AllowNoKey 时仍 fail-closed。
func TestListModelsAllowNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("无 key 时不应发送 Authorization, got %q", got)
		}
		w.Write([]byte(`{"data":[{"id":"local-1"}]}`))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(&Profile{Name: "custom", BaseURL: srv.URL}, ClientOptions{AllowNoKey: true})
	if err != nil {
		t.Fatalf("AllowNoKey 应允许无 key 构造: %v", err)
	}
	ids, err := c.ListModels(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != "local-1" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := NewClient(&Profile{Name: "custom", BaseURL: srv.URL}, ClientOptions{}); err == nil {
		t.Fatal("未设 AllowNoKey 时无 key 必须报错")
	}
}

func TestListModelsEmptyFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	c, _ := NewClient(&Profile{Name: "test", BaseURL: srv.URL}, ClientOptions{APIKey: "k1"})
	if _, err := c.ListModels(context.Background()); err == nil {
		t.Fatal("空模型列表必须报错，不返回空列表冒充")
	}
}

func TestRegistryAliases(t *testing.T) {
	// zai ↔ glm（内置别名）
	if p := Lookup("zai"); p == nil || p.Name != "zai" {
		t.Fatal("Lookup(zai) 失败")
	}
	if p := Lookup("GLM"); p == nil || p.Name != "zai" {
		t.Fatal("Lookup(GLM) 别名解析失败")
	}
	if p := Lookup("deepseek"); p == nil {
		t.Fatal("Lookup(deepseek) 失败")
	}
	if Lookup("nonexistent") != nil {
		t.Fatal("未知名字必须返回 nil")
	}
	if Lookup("") != nil {
		t.Fatal("空名字必须返回 nil")
	}
}

func TestRegisterOverrides(t *testing.T) {
	// 后注册覆盖（用户自定义 provider 覆盖内置同名）。
	override := &Profile{Name: "zai", BaseURL: "http://127.0.0.1:1", EnvVars: []string{"GLM_API_KEY"}, DefaultModel: "overridden"}
	if err := Register(override); err != nil {
		t.Fatal(err)
	}
	if p := Lookup("zai"); p.BaseURL != "http://127.0.0.1:1" {
		t.Errorf("后注册未覆盖: %+v", p)
	}
	// 恢复内置，避免影响其它测试。
	if err := Register(Builtins()[0]); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAPIKeyPriority(t *testing.T) {
	t.Setenv("AI_PRIORITY_1", "")
	t.Setenv("AI_PRIORITY_2", "second-wins")
	p := &Profile{Name: "keytest", EnvVars: []string{"AI_PRIORITY_1", "AI_PRIORITY_2"}}
	if got := ResolveAPIKey(p); got != "second-wins" {
		t.Errorf("env 优先级解析 = %q, want second-wins", got)
	}
	t.Setenv("AI_PRIORITY_1", "first-wins")
	if got := ResolveAPIKey(p); got != "first-wins" {
		t.Errorf("env 优先级解析 = %q, want first-wins", got)
	}
}

func TestNewClientMissingKeyFailsClosed(t *testing.T) {
	t.Setenv("AI_NO_KEY_ENV", "")
	p := &Profile{Name: "nokey", BaseURL: "http://127.0.0.1:1", EnvVars: []string{"AI_NO_KEY_ENV"}}
	if _, err := NewClient(p, ClientOptions{}); err == nil {
		t.Fatal("无 key 必须 fail-closed")
	}
}

func TestCustomBaseURLOverride(t *testing.T) {
	srv, got, _ := fakeOpenAI(t)
	p := &Profile{Name: "custom", BaseURL: "http://127.0.0.1:1", EnvVars: []string{"AI_CUSTOM_API_KEY"}}
	c, err := NewClient(p, ClientOptions{BaseURL: srv.URL, APIKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if *got == nil || !strings.HasPrefix((*got).URL.Path, "/chat/completions") {
		t.Error("base_url 覆盖未生效")
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	if _, err := Resolve("does-not-exist"); err == nil {
		t.Fatal("未知 provider 必须报错")
	}
}

func TestProviderNames(t *testing.T) {
	names := ProviderNames()
	found := false
	for _, n := range names {
		if n == "zai" {
			found = true
		}
	}
	if !found {
		t.Errorf("ProviderNames 里没有 zai: %v", names)
	}
}

func TestResolveAPIKeysMulti(t *testing.T) {
	t.Setenv("MULTI_A", "env1")
	t.Setenv("MULTI_B", "env2,env3") // 逗号分隔多 key
	p := &Profile{
		Name:    "multi",
		EnvVars: []string{"MULTI_A", "MULTI_B"},
		Keys:    []string{"explicit1", "env1"}, // 显式 + 与 env 重复项
	}
	got := ResolveAPIKeys(p, "explicit0")
	want := []string{"explicit0", "explicit1", "env1", "env2", "env3"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pos %d = %q want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

// TestChatRotatesOn429：provider 持两个 key（bad,good），bad 被 429 打回 →
// 应自动轮换到 good 并收敛成功（学 Hermes 多 key 轮换）。
func TestChatRotatesOn429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer bad":
			w.WriteHeader(http.StatusTooManyRequests)
		case "Bearer good":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"rotated-ok"},"finish_reason":"stop"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	if err := Register(&Profile{Name: "testrot", EnvVars: []string{"TESTROT_KEY"}, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTROT_KEY", "bad,good")
	resp, err := Chat(context.Background(), "testrot",
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, ClientOptions{})
	if err != nil {
		t.Fatalf("轮换后应收敛成功: %v", err)
	}
	if resp.Content != "rotated-ok" {
		t.Fatalf("Content = %q, want rotated-ok（应已轮换到 good key）", resp.Content)
	}
}

// TestChatFallbackProvider：主 provider 无 key，回退 provider 有 key → 应自动切换。
func TestChatFallbackProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"fb-ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	if err := Register(&Profile{Name: "testfb", EnvVars: []string{"TESTFB_KEY"}, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := Register(&Profile{Name: "testpf", EnvVars: []string{"TESTPF_KEY"}, Fallbacks: []string{"testfb"}, BaseURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTPF_KEY", "")
	t.Setenv("TESTFB_KEY", "fbkey")
	resp, err := Chat(context.Background(), "testpf",
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, ClientOptions{})
	if err != nil {
		t.Fatalf("回退应收敛成功: %v", err)
	}
	if resp.Content != "fb-ok" {
		t.Fatalf("Content = %q, want fb-ok（应已回退到 testfb）", resp.Content)
	}
}

// TestChatNonRetryableStops：400（请求本身坏）不应进入重试循环，立即终止。
func TestChatNonRetryableStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	t.Cleanup(srv.Close)
	if err := Register(&Profile{Name: "test400", EnvVars: []string{"TEST400_KEY"}, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST400_KEY", "onlykey")
	_, err := Chat(context.Background(), "test400",
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, ClientOptions{})
	if err == nil {
		t.Fatal("400 必须失败")
	}
	if strings.Contains(err.Error(), "全部 key/回退均失败") {
		t.Fatalf("400 不该走重试循环，应不可重试即止: %v", err)
	}
}
