package main

// agentcli_test.go —— forge agent 接线的端到端测试（httptest 假 provider，零网络）。
//
// 覆盖：chat 单发 + 用量行、写工具白名单 fail-closed 与放行后落盘、serve 记忆
// 回读注入 system、按回合技能注入（loop 级）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// newFakeProvider 起一个 OpenAI-compatible 假服务器：按调用次序返回预置响应体。
func newFakeProvider(t *testing.T, resps ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := calls
		calls++
		mu.Unlock()
		if i >= len(resps) {
			i = len(resps) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resps[i]))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newEchoSystemProvider 起一个把 system 消息回显为 content 的假服务器
// （断言 system 注入用：记忆摘要 / persona / 技能都拼在首条消息里）。
func newEchoSystemProvider(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		sys := ""
		if len(req.Messages) > 0 {
			sys = req.Messages[0].Content
		}
		sysJSON, _ := json.Marshal(sys)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"3","model":"m","choices":[{"message":{"role":"assistant","content":` +
			string(sysJSON) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const respFinal = `{"id":"1","model":"custom-model","choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

const respMemSetToolCall = `{"id":"2","model":"custom-model","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"mem.set","arguments":"{\"key\":\"project\",\"value\":\"低温数据补全\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}`

// chat 单发：stdout 只放最终回复；stderr 有真实用量行；价目未知 ⇒ cost 显示 —（不是 $0）。
func TestAgentChatOfflineBasicAndUsage(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := newFakeProvider(t, respFinal)

	var out, errW bytes.Buffer
	o := agentOpts{provider: "custom", baseURL: srv.URL, usageW: &errW}
	code := runAgentChat(context.Background(), o, "你好", false, &out, &errW)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errW.String())
	}
	if got := strings.TrimSpace(out.String()); got != "pong" {
		t.Fatalf("stdout 应只有最终回复, got %q", got)
	}
	for _, want := range []string{"# usage: calls=1", "prompt=5 completion=2 cached=0 total=7", "cost=—(价目未知)"} {
		if !strings.Contains(errW.String(), want) {
			t.Fatalf("用量行缺 %q: %q", want, errW.String())
		}
	}
	if !strings.Contains(errW.String(), "cost=—(价目未知)") {
		t.Fatalf("价目未知应显示 — 而非 0: %q", errW.String())
	}
}

// 写工具缺省全只读：模型调 mem.set ⇒ fail-closed（exit 1）；显式 --allow-writes 后
// 成功且记忆落盘（重开文件读回）。
func TestAgentChatOfflineWriteGateAndPersist(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")

	memDir := t.TempDir()
	// 1) 未放行 ⇒ 写被拒（fail-closed），模型收到拒绝原因后继续收尾；
	//    关键断言：**写没有发生**（记忆不落盘）。
	srv1 := newFakeProvider(t, respMemSetToolCall, respFinal)
	var out1, err1 bytes.Buffer
	o1 := agentOpts{provider: "custom", baseURL: srv1.URL, memoryDir: memDir, usageW: &err1}
	if code := runAgentChat(context.Background(), o1, "记住 project", false, &out1, &err1); code != 0 {
		t.Fatalf("写被拒后回合应正常收敛: code=%d stderr=%s", code, err1.String())
	}
	if _, err := os.Stat(filepath.Join(memDir, "default.json")); !os.IsNotExist(err) {
		t.Fatalf("被拒会话不应落盘记忆: %v", err)
	}

	// 2) 显式放行 mem.set ⇒ 成功 + 落盘
	srv2 := newFakeProvider(t, respMemSetToolCall, respFinal)
	var out2, err2 bytes.Buffer
	o2 := agentOpts{provider: "custom", baseURL: srv2.URL, memoryDir: memDir,
		allowWrites: []string{"mem.set"}, usageW: &err2}
	if code := runAgentChat(context.Background(), o2, "记住 project", false, &out2, &err2); code != 0 {
		t.Fatalf("放行后应成功: code=%d stderr=%s", code, err2.String())
	}
	ms, err := agent.NewMemStoreFile(filepath.Join(memDir, "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := ms.Get("project"); !ok || v != "低温数据补全" {
		t.Fatalf("记忆应已落盘, got %q ok=%v", v, ok)
	}
}

// serve：已持久化记忆在会话构造时注入 system（回读验证），协议行纯净（quit 有响应）。
func TestAgentServeOfflineMemoryRoundTrip(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")

	memDir := t.TempDir()
	seed, err := agent.NewMemStoreFile(filepath.Join(memDir, "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	seed.Set("k", "v")

	echoSrv := newEchoSystemProvider(t)
	o := agentOpts{provider: "custom", baseURL: echoSrv.URL, memoryDir: memDir}
	h := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o) })
	in := strings.NewReader("{\"msg\":\"hi\"}\n{\"quit\":true}\n")
	var out bytes.Buffer
	if err := h.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "k=v") {
		t.Fatalf("已持久化记忆应注入 system（回读）, got: %s", out.String())
	}
	if !strings.Contains(out.String(), `"quit":true`) {
		t.Fatalf("quit 应有响应行: %s", out.String())
	}
}

// loop 级：技能按回合注入 system，且不跨回合叠加（每回合基于 baseSystem 重算）。
func TestSessionSkillsInjectPerTurnNotStacked(t *testing.T) {
	dir := t.TempDir()
	skillFile := filepath.Join(dir, "cryo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("---\nname: cryo\ndescription: superconductivity experiment guide\n---\nCRYO_BODY_MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var systems []string
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		mu.Lock()
		systems = append(systems, req.Messages[0].Content)
		mu.Unlock()
		return &ai.ChatResponse{Content: "ok"}, nil
	}
	s := agent.NewSession(agent.DefaultConfig(), chat, agent.NewRegistry(), "BASE")
	s.Skills = agent.NewSkills([]string{dir}, agent.NewKeywordMatcher())

	if _, err := s.Turn(context.Background(), "explain the superconductivity experiment"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(systems[0], "CRYO_BODY_MARKER") {
		t.Fatalf("命中技能应注入 system: %q", systems[0])
	}
	if _, err := s.Turn(context.Background(), "unrelated message"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(systems[1], "CRYO_BODY_MARKER") {
		t.Fatalf("未命中回合不应残留上一回合注入（不应叠加）: %q", systems[1])
	}
	if !strings.Contains(systems[1], "BASE") {
		t.Fatalf("system 应回到基线: %q", systems[1])
	}
}

// newCountMessagesProvider 返回"消息条数"作为回复：用来证明历史被恢复/累加。
func newCountMessagesProvider(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		content, _ := json.Marshal(strconv.Itoa(len(req.Messages)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":` +
			string(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --state-dir：第二次 chat 必须接着上一次的消息栈（跨进程恢复），文件 0600。
func TestAgentChatStateDirResumesHistory(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := newCountMessagesProvider(t)
	dir := t.TempDir()
	o := agentOpts{provider: "custom", baseURL: srv.URL, stateDir: dir}

	var out1, err1 bytes.Buffer
	if code := runAgentChat(context.Background(), o, "第一句", false, &out1, &err1); code != 0 {
		t.Fatalf("首轮失败: code=%d stderr=%s", code, err1.String())
	}
	// 首轮请求体 = system + user = 2
	if got := strings.TrimSpace(out1.String()); got != "2" {
		t.Fatalf("首轮应 2 条消息, got %q", got)
	}

	var out2, err2 bytes.Buffer
	if code := runAgentChat(context.Background(), o, "第二句", false, &out2, &err2); code != 0 {
		t.Fatalf("第二轮失败: code=%d stderr=%s", code, err2.String())
	}
	// 恢复后 = system + u1 + a1 + u2 = 4
	if got := strings.TrimSpace(out2.String()); got != "4" {
		t.Fatalf("恢复后应 4 条消息, got %q（历史没接上？）", got)
	}
	if !strings.Contains(err2.String(), "恢复 2 条历史") {
		t.Fatalf("应在 stderr 报告恢复条数: %q", err2.String())
	}

	// 历史含用户输入 ⇒ 0600，不能是 0644。
	st, err := os.Stat(filepath.Join(dir, "default.history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("历史文件权限应 0600, got %04o", perm)
	}
}

// 历史文件损坏：明说并从空开始，不崩、不静默。
func TestAgentChatStateDirCorruptStartsEmpty(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := newCountMessagesProvider(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "default.history.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errW bytes.Buffer
	o := agentOpts{provider: "custom", baseURL: srv.URL, stateDir: dir}
	if code := runAgentChat(context.Background(), o, "hi", false, &out, &errW); code != 0 {
		t.Fatalf("损坏历史不应让运行失败: code=%d stderr=%s", code, errW.String())
	}
	if got := strings.TrimSpace(out.String()); got != "2" {
		t.Fatalf("应从头开始（2 条）, got %q", got)
	}
	if !strings.Contains(errW.String(), "历史读取失败") {
		t.Fatalf("应明说历史读取失败: %q", errW.String())
	}
}

// serve + --state-dir：起两次 serve 进程，第二次必须看到第一次的消息栈。
func TestAgentServeStateDirResumesHistory(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := newCountMessagesProvider(t)
	dir := t.TempDir()
	o := agentOpts{provider: "custom", baseURL: srv.URL, stateDir: dir}

	runOnce := func(msg string) string {
		h := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o) })
		h.AfterTurn = func(id string, s *agent.Session) {
			if err := saveAgentHistory(agentStatePath(o.stateDir, id), s); err != nil {
				t.Fatalf("落盘失败: %v", err)
			}
		}
		in := strings.NewReader(`{"msg":"` + msg + `"}` + "\n" + `{"quit":true}` + "\n")
		var out bytes.Buffer
		if err := h.Serve(context.Background(), in, &out); err != nil {
			t.Fatalf("Serve: %v", err)
		}
		return out.String()
	}

	if first := runOnce("第一"); !strings.Contains(first, `"content":"2"`) {
		t.Fatalf("首轮应 2 条消息（system+user）, got %s", first)
	}
	if second := runOnce("第二"); !strings.Contains(second, `"content":"4"`) {
		t.Fatalf("重启后应 4 条消息（system+u1+a1+u2）, got %s", second)
	}
}

// serve + {"stream":true}：一行请求产出多行（delta…done），最终含完整内容。
func TestAgentServeStreamEmitsDeltas(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	o := agentOpts{provider: "custom", baseURL: srv.URL}
	h := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o) })
	in := strings.NewReader(`{"msg":"hi","stream":true}` + "\n" + `{"quit":true}` + "\n")
	var out bytes.Buffer
	if err := h.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"type":"delta"`) {
		t.Fatalf("应含 delta 行: %s", got)
	}
	if !strings.Contains(got, `"type":"done"`) {
		t.Fatalf("应含 done 行: %s", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Fatalf("done 应含最终内容 Hello: %s", got)
	}
	// 非流式契约不受影响：不带 stream 仍是单行 content。
	o2 := agentOpts{provider: "custom", baseURL: srv.URL}
	h2 := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o2) })
	in2 := strings.NewReader(`{"msg":"hi"}` + "\n" + `{"quit":true}` + "\n")
	var out2 bytes.Buffer
	if err := h2.Serve(context.Background(), in2, &out2); err != nil {
		t.Fatalf("Serve2: %v", err)
	}
	if strings.Contains(out2.String(), `"type":"delta"`) {
		t.Fatalf("非流式不应出现 delta 行: %s", out2.String())
	}
}

// chat --stream：增量直接落 stdout（可管道），再补一行完整内容。
func TestAgentChatStreamPrintsDeltas(t *testing.T) {
	ai.RegisterBuiltins()
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	o := agentOpts{provider: "custom", baseURL: srv.URL}
	var out, errW bytes.Buffer
	if code := runAgentChat(context.Background(), o, "hi", true, &out, &errW); code != 0 {
		t.Fatalf("chat --stream 失败: code=%d stderr=%s", code, errW.String())
	}
	if got := out.String(); !strings.Contains(got, "Hello") {
		t.Fatalf("stdout 应含逐字增量拼成的 Hello: %q", got)
	}
}

func TestAgentSafeName(t *testing.T) {
	cases := map[string]string{
		"default":    "default",
		"session-01": "session-01",
		"a/b":        "a_b",
		"../evil":    "___evil",
		"":           "default",
		"中文会话":       "____",
	}
	for in, want := range cases {
		if got := agentSafeName(in); got != want {
			t.Fatalf("agentSafeName(%q)=%q want %q", in, got, want)
		}
	}
}
