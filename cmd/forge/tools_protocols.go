package main

// tools_protocols.go —— ACP（Agent Client Protocol）与 DIAP（去中心化身份/能力）。
//
// ACP：配置 ~/.forge/acp.json，把任意 ACP server（如 `hermes acp`）接成 acp.prompt 工具。
// DIAP：直接复用 internal/identity 的 did:key + ed25519 + 能力令牌，不发明第二套身份。

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/identity"
)

type diapRuntime struct {
	mu sync.Mutex
	id *identity.Identity
}

func (d *diapRuntime) ensure() (*identity.Identity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.id != nil {
		return d.id, nil
	}
	id, err := identity.Generate()
	if err != nil {
		return nil, err
	}
	d.id = id
	return id, nil
}

// ---- ACP ----

type acpConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Cwd     string   `json:"cwd"`
}

type acpClient struct {
	rw     io.ReadWriteCloser
	br     *bufio.Reader
	mu     sync.Mutex
	nextID int64
}

func (c *acpClient) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n') // ACP 使用换行分隔的 JSON-RPC
	_, err = c.rw.Write(b)
	return err
}

type rpcEnvelope struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *acpClient) read() (rpcEnvelope, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return rpcEnvelope{}, err
	}
	line = strings.TrimRight(line, "\r\n")
	// 兼容 Content-Length 分帧（部分 ACP 实现沿用 LSP 头）。
	if strings.HasPrefix(strings.ToLower(line), "content-length:") {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:")))
		for {
			h, err := c.br.ReadString('\n')
			if err != nil {
				return rpcEnvelope{}, err
			}
			if strings.TrimSpace(h) == "" {
				break
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.br, buf); err != nil {
			return rpcEnvelope{}, err
		}
		line = string(buf)
	}
	var env rpcEnvelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return rpcEnvelope{}, fmt.Errorf("acp: 解析消息失败: %w", err)
	}
	return env, nil
}

func (c *acpClient) request(ctx context.Context, method string, params any, onNote func(string, json.RawMessage)) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		env, err := c.read()
		if err != nil {
			return nil, err
		}
		if env.Method != "" && env.ID == nil {
			if onNote != nil {
				onNote(env.Method, env.Params)
			}
			continue
		}
		if env.ID != nil && *env.ID == id {
			if env.Error != nil {
				return nil, fmt.Errorf("acp: %s 失败: %s", method, env.Error.Message)
			}
			return env.Result, nil
		}
	}
}

func acpConfigPath(base string) string {
	if p := os.Getenv("HUSHFUSION_ACP_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(runtimeRoot(base), "acp.json")
}

func acpPrompt(ctx context.Context, base, prompt string) (string, error) {
	path := acpConfigPath(base)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("acp: 未配置（%s）；示例：{\"command\":\"hermes\",\"args\":[\"acp\"]}", path)
	}
	var cfg acpConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("acp: 配置解析失败: %w", err)
	}
	if cfg.Command == "" {
		return "", fmt.Errorf("acp: 配置缺 command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Dir = cfg.Cwd
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("acp: 启动失败: %w", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	c := &acpClient{rw: &mcpStdio{r: stdout, w: stdin}, br: bufio.NewReader(stdout)}
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": true, "writeTextFile": false}},
	}, nil); err != nil {
		return "", err
	}
	res, err := c.request(ctx, "session/new", map[string]any{
		"cwd": cfg.Cwd, "mcpServers": []any{},
	}, nil)
	if err != nil {
		return "", err
	}
	var sess struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &sess); err != nil || sess.SessionID == "" {
		return "", fmt.Errorf("acp: session/new 未返回 sessionId: %s", string(res))
	}
	var out strings.Builder
	_, err = c.request(ctx, "session/prompt", map[string]any{
		"sessionId": sess.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": prompt}},
	}, func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var u struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(params, &u) == nil && u.Update.SessionUpdate == "agent_message_chunk" {
			out.WriteString(u.Update.Content.Text)
		}
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// ---- 注册 ----

func registerProtocolTools(s *agent.Session, base string) {
	diap := &diapRuntime{}
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("diap.identity", "生成/复用本会话的 DID 身份（did:key + ed25519）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			id, err := diap.ensure()
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{
				"did":        id.DID(),
				"public_key": base64.RawStdEncoding.EncodeToString(id.PublicKey()),
			})
		},
	})

	register(agent.Tool{
		Spec: projectTool("diap.issue", "签发 DIAP 能力令牌（subject + caps + ttl）",
			map[string]any{
				"subject":     map[string]any{"type": "string", "description": "被授权者 DID"},
				"caps":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "能力名，如 kernel.write"},
				"ttl_seconds": map[string]any{"type": "integer", "description": "有效期秒数，缺省 3600；0=不过期"},
			}, []string{"subject", "caps"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			id, err := diap.ensure()
			if err != nil {
				return "", err
			}
			subject := argString(args, "subject", "")
			caps := argStrings(args, "caps")
			if subject == "" || len(caps) == 0 {
				return "", fmt.Errorf("diap.issue: subject/caps 不能为空")
			}
			ttl := time.Duration(argInt(args, "ttl_seconds", 3600)) * time.Second
			tok, err := id.Issue(subject, caps, ttl)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{"token": string(tok), "issuer": id.DID()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("diap.authorize", "验签并检查 DIAP 能力令牌",
			map[string]any{
				"token":      map[string]any{"type": "string"},
				"subject":    map[string]any{"type": "string"},
				"capability": map[string]any{"type": "string"},
			}, []string{"token", "subject", "capability"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			tok := identity.CapabilityToken(argString(args, "token", ""))
			cap, err := identity.Authorize(tok, argString(args, "subject", ""), argString(args, "capability", ""), time.Now())
			if err != nil {
				return "", err
			}
			return jsonText(cap)
		},
	})

	register(agent.Tool{
		Spec: projectTool("acp.status", "查看 ACP server 配置状态", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			path := acpConfigPath(base)
			_, err := os.Stat(path)
			return jsonText(map[string]any{"path": path, "configured": err == nil})
		},
	})

	register(agent.Tool{
		Spec: projectTool("acp.prompt", "通过 ACP（Agent Client Protocol）让外部 agent 执行一个 prompt",
			map[string]any{"prompt": map[string]any{"type": "string"}}, []string{"prompt"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			prompt := argString(args, "prompt", "")
			if prompt == "" {
				return "", fmt.Errorf("acp.prompt: prompt 为空")
			}
			runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			out, err := acpPrompt(runCtx, base, prompt)
			if err != nil {
				return "", err
			}
			return out, nil
		},
	})
}
