package main

// tools_bolloon.go —— 把本机 BOLLOON 去中心化智能体 CLI 暴露成模型工具。
//
// BOLLOON 负责：DID/入网、agent 发现、任务板、任务消息、P2P 节点、
// 以及把 prompt 委派给外部编码智能体（engine run）。
// 所有工具都走 internal/bolloon 的 --json 信封；ok:false 一律 fail-closed。
// 二进制缺失时明确要求安装/设置 BOLLOON_BIN，不静默降级。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/bolloon"
)

func bolloonClient() (*bolloon.Client, error) {
	c, err := bolloon.New()
	if err != nil {
		return nil, fmt.Errorf("%w；安装 `npm i -g @bolloon/bolloon-agent`，或设置 BOLLOON_BIN 指到二进制", err)
	}
	return c, nil
}

// envelopeView 把 BOLLOON 信封转成可读 JSON（data 原样展开）。
func envelopeView(env *bolloon.Envelope) (map[string]any, error) {
	var data any
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			data = string(env.Data)
		}
	} else if len(env.Raw) > 0 {
		// p2p --json 等把字段放在顶层；没有 data 时退回完整原文。
		if err := json.Unmarshal(env.Raw, &data); err != nil {
			data = string(env.Raw)
		}
	}
	return map[string]any{
		"ok": env.OK, "code": env.Code, "message": env.Message,
		"next_action": env.NextAction, "data": data,
	}, nil
}

func registerBolloonTools(s *agent.Session, _ string) {
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("bolloon.status", "BOLLOON 去中心化入网态：DID、已加入网络、网关 peerId/networkLink",
			nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.Run(ctx, "network", "status")
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.join", "加入外部 BOLLOON 网络（networkLink）；会建立去中心化 P2P 连接",
			map[string]any{"link": map[string]any{"type": "string", "description": "bolloon networkLink"}},
			[]string{"link"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			link := argString(args, "link", "")
			if link == "" {
				return "", fmt.Errorf("bolloon.join: link 为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.NetworkJoin(ctx, link)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.discover", "发现网络上的外部 BOLLOON agent（外部智能体）", nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.AgentDiscover(ctx)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.board", "读取 BOLLOON 任务板（外部智能体发布的协作任务）",
			map[string]any{"capability": map[string]any{"type": "string", "description": "可选：按能力过滤，如 fusion"}},
			nil),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			board, err := c.TaskBoard(ctx, argString(args, "capability", ""))
			if err != nil {
				return "", err
			}
			return jsonText(board)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.publish", "向 BOLLOON 网络发布一条协作任务公告（公告 JSON 文件路径）",
			map[string]any{"path": map[string]any{"type": "string", "description": "announcement JSON 文件"}},
			[]string{"path"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := argString(args, "path", "")
			if path == "" {
				return "", fmt.Errorf("bolloon.publish: path 为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.TaskPublish(ctx, path)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.claim", "认领 BOLLOON 任务板上的开放任务",
			map[string]any{"announcement_id": map[string]any{"type": "string"}},
			[]string{"announcement_id"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			id := argString(args, "announcement_id", "")
			if id == "" {
				return "", fmt.Errorf("bolloon.claim: announcement_id 为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.TaskClaim(ctx, id)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.send", "向 BOLLOON 任务的参与者发消息（去中心化智能体交流）",
			map[string]any{
				"announcement_id": map[string]any{"type": "string"},
				"message":         map[string]any{"type": "string"},
			}, []string{"announcement_id", "message"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			id := argString(args, "announcement_id", "")
			msg := argString(args, "message", "")
			if id == "" || msg == "" {
				return "", fmt.Errorf("bolloon.send: announcement_id/message 不能为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.TaskSend(ctx, id, msg)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.engine", "把一段 prompt 委派给外部编码智能体执行（会消耗外部 agent 资源）",
			map[string]any{"prompt": map[string]any{"type": "string"}},
			[]string{"prompt"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			prompt := argString(args, "prompt", "")
			if prompt == "" {
				return "", fmt.Errorf("bolloon.engine: prompt 为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.EngineRun(ctx, prompt)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.p2p", "启动/读取本机 BOLLOON P2P 节点，让外部智能体能主动发消息进来", nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.P2P(ctx)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})

	register(agent.Tool{
		Spec: projectTool("bolloon.register", "把本系统注册为 BOLLOON agent（manifest 文件路径）",
			map[string]any{"manifest": map[string]any{"type": "string", "description": "agent manifest JSON 路径"}},
			[]string{"manifest"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			manifest := argString(args, "manifest", "")
			if manifest == "" {
				return "", fmt.Errorf("bolloon.register: manifest 为空")
			}
			c, err := bolloonClient()
			if err != nil {
				return "", err
			}
			env, err := c.AgentRegister(ctx, manifest)
			if err != nil {
				return "", err
			}
			v, _ := envelopeView(env)
			return jsonText(v)
		},
	})
}
