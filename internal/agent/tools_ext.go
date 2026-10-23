package agent

// tools_ext.go —— 内置轻工具集（内存型，零依赖）：记忆 / 任务 / 技能查看。
//
// 对应 Hermes 功能面：智能体记忆（/memory）、目标/待办/计划（/goal /tasks）、
// 技能查看（/skills）。这些工具是 Session 状态（MemStore/TaskStore/SkillsDir）的
// 模型可见入口 —— 模型经 ToolRegistry 统一纪律调用（fail-closed 三闸不变）。
//
// 写工具标记 Write=true：AI 调用它们必须先在会话白名单（AllowWrites）里放行。
// 只有读工具（Get/List/View）默认可用（只读不需要白名单）。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// toolArgs 是工具参数 schema（JSON Schema 子集：object + string 属性）。
func toolArgs(props map[string]string, required []string) any {
	pp := map[string]any{}
	for name, desc := range props {
		pp[name] = map[string]any{"type": "string", "description": desc}
	}
	out := map[string]any{"type": "object", "properties": pp}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func mkSpec(name, desc string, props map[string]string, required []string) ai.ToolSpec {
	ts := ai.ToolSpec{Type: "function"}
	ts.Function.Name = name
	ts.Function.Description = desc
	ts.Function.Parameters = toolArgs(props, required)
	return ts
}

// registerMemoryTools 注册记忆工具（mem.get 只读；mem.set / mem.delete 写）。
func registerMemoryTools(r *ToolRegistry, s *Session) error {
	// mem.get —— 只读，无需白名单。
	if err := r.Register(Tool{
		Spec:  mkSpec("mem.get", "读取一条会话记忆（key=value 形式；不存在返回空）", map[string]string{"key": "记忆键名"}, []string{"key"}),
		Write: false,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			if strings.TrimSpace(key) == "" {
				return "", fmt.Errorf("mem.get: key 为空")
			}
			if v, ok := s.Memory.Get(key); ok {
				return key + "=" + v, nil
			}
			return key + "=（不存在）", nil
		},
	}); err != nil {
		return err
	}
	// mem.set —— 写，需白名单。
	if err := r.Register(Tool{
		Spec:  mkSpec("mem.set", "写入一条会话记忆（覆盖同名；值会随对话历史发给模型，不要存秘密）", map[string]string{"key": "记忆键名", "value": "记忆值"}, []string{"key", "value"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			val, _ := args["value"].(string)
			if strings.TrimSpace(key) == "" {
				return "", fmt.Errorf("mem.set: key 为空")
			}
			s.Memory.Set(key, val)
			return "已记忆 " + key + "=" + val, nil
		},
	}); err != nil {
		return err
	}
	// mem.reset —— 写，清空全部记忆。
	if err := r.Register(Tool{
		Spec:  mkSpec("mem.reset", "清空全部会话记忆", nil, nil),
		Write: true,
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			n := s.Memory.Len()
			s.Memory.Reset()
			return fmt.Sprintf("已清空 %d 条记忆", n), nil
		},
	}); err != nil {
		return err
	}
	return nil
}

// registerTaskTools 注册任务/目标工具（tasks.list 只读；其余写）。
func registerTaskTools(r *ToolRegistry, s *Session) error {
	// tasks.list —— 只读。
	if err := r.Register(Tool{
		Spec:  mkSpec("tasks.list", "列出当前目标/待办/任务（proposed/active/blocked/done/dropped；含证据）", nil, nil),
		Write: false,
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			all := s.Tasks.All()
			if len(all) == 0 {
				return "（无任务）", nil
			}
			lines := make([]string, 0, len(all))
			for _, t := range all {
				lines = append(lines, fmt.Sprintf("[%s] %s %s — %s", t.State, t.ID, t.Goal, t.Title))
				if len(t.Evidence) > 0 {
					lines = append(lines, "    证据: "+strings.Join(t.Evidence, "; "))
				}
			}
			return strings.Join(lines, "\n"), nil
		},
	}); err != nil {
		return err
	}
	// tasks.add —— 写。
	if err := r.Register(Tool{
		Spec:  mkSpec("tasks.add", "新增一条目标/任务/待办（proposed 态）", map[string]string{"title": "任务标题", "goal": "所属长期目标（可空）"}, []string{"title"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			goal, _ := args["goal"].(string)
			if strings.TrimSpace(title) == "" {
				return "", fmt.Errorf("tasks.add: title 为空")
			}
			id := s.Tasks.Add(title, goal)
			return "已新增 " + id + "（proposed）", nil
		},
	}); err != nil {
		return err
	}
	// tasks.set_state —— 写；done 必须附证据（完成只能被证明，不能被声称）。
	if err := r.Register(Tool{
		Spec: mkSpec("tasks.set_state", "推进任务状态机；转 done 必须附证据路径；AI 不能自己声称完成（见纪律）",
			map[string]string{"id": "任务 id (A-001)", "state": "目标状态 proposed/active/blocked/done/dropped", "evidence": "证据路径（转 done 必填，如 runs/xxx/report.md）"}, []string{"id", "state"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			state, _ := args["state"].(string)
			evidence, _ := args["evidence"].(string)
			if err := s.Tasks.SetState(id, TaskState(state), "agent", evidence); err != nil {
				return "", err
			}
			return "任务 " + id + " → " + state, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

// registerDelegateTool 注册 delegate（把子任务派给独立子智能体）。
//
// 关键纪律：子会话的嵌套深度 = 父 +1，>0 时 RegisterBuiltinTools 不再注册 delegate，
// 从结构上杜绝「委派里再委派 → 自递归」的失控（这正是用户担心的后台自递归来源之一）。
// 子会话独立预算（≤ 父一半，可请求更小）、独立上下文窗口、继承父的 provider/model 与
// 写白名单；返回最终文本 + 已并入父账本的用量。失败回灌父会话自愈，绝不假装完成。
func (s *Session) registerDelegateTool() error {
	spec := mkSpec("delegate",
		"把子任务派给一个独立的子智能体（独立预算、独立上下文窗口、可指定 provider/model）。"+
			"适合可独立验证的子目标；不要委派需要你亲自协调的连环任务。返回子智能体最终文本。",
		map[string]string{
			"task":          "派给子智能体的子任务（必填，自包含、可验证）",
			"provider":      "可选：子智能体用的 provider（默认继承父）",
			"model":         "可选：子智能体用的模型（默认继承父）",
			"budget_tokens": "可选：子智能体预算上限（默认父的一半，且不超过此值）",
		},
		[]string{"task"})
	spec.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":          map[string]any{"type": "string", "description": "派给子智能体的子任务（必填）"},
			"provider":      map[string]any{"type": "string", "description": "可选 provider"},
			"model":         map[string]any{"type": "string", "description": "可选模型"},
			"budget_tokens": map[string]any{"type": "integer", "description": "可选预算（token）"},
		},
		"required": []string{"task"},
	}
	return s.registry.Register(Tool{
		Spec:     spec,
		Executor: s.delegateExecute,
	})
}

// delegateExecute 实现一个子智能体：独立 Session + 独立预算，跑完返回文本。
func (s *Session) delegateExecute(ctx context.Context, _ string, args map[string]any) (string, error) {
	if s.nesting >= 1 {
		return "", errors.New("delegate: 已达委派嵌套上限（禁止自递归）")
	}
	task, _ := args["task"].(string)
	task = strings.TrimSpace(task)
	if task == "" {
		return "", errors.New("delegate: task 为空")
	}
	provider := s.cfg.Provider
	if p, ok := args["provider"].(string); ok && p != "" {
		provider = p
	}
	model := s.cfg.Model
	if m, ok := args["model"].(string); ok && m != "" {
		model = m
	}
	// 子预算：默认父的一半；请求更小则取请求值；绝不超父一半。
	childBudget := s.cfg.MaxBudgetTokens / 2
	if b, ok := args["budget_tokens"].(float64); ok && b > 0 {
		ib := int(b)
		if ib < childBudget {
			childBudget = ib
		}
	}
	childCfg := DefaultConfig()
	childCfg.Provider = provider
	childCfg.Model = model
	childCfg.MaxBudgetTokens = childBudget
	child := NewSession(childCfg, s.chat, NewRegistry(), s.systemText())
	child.SetNesting(s.nesting + 1)
	if s.chatStream != nil {
		child.SetStreamChat(s.chatStream)
	}
	if err := registerMemoryTools(child.registry, child); err != nil {
		return "", fmt.Errorf("delegate: 子会话工具注册失败: %w", err)
	}
	if err := registerTaskTools(child.registry, child); err != nil {
		return "", fmt.Errorf("delegate: 子会话工具注册失败: %w", err)
	}
	child.AllowWrites = s.AllowWrites // 继承父权限（通常仍全只读）
	modelOr := model
	if modelOr == "" {
		modelOr = "(默认)"
	}
	out, err := child.Turn(ctx, task)
	if err != nil {
		return "", fmt.Errorf("delegate: 子智能体失败: %w", err)
	}
	// 子会话用量并入父账本（审计/成本守恒，避免「看不见的二次调用」）。
	s.recordUsage(child.LastUsage)
	return fmt.Sprintf("[delegate %s/%s · 子预算 %d token · 实耗 %d token]\n%s",
		provider, modelOr, childBudget, child.LastUsage.TotalTokens, out), nil
}

// registerSkillView 注册技能查看工具（skill.view —— 只读：按名字取技能正文）。
func registerSkillView(r *ToolRegistry, skills *SkillsDir) error {
	if skills == nil {
		return nil // 无技能目录 → 不注册（工具可选，不假装有）
	}
	return r.Register(Tool{
		Spec:  mkSpec("skill.view", "加载一个技能的完整正文（对话开始时只注入描述；需要细节时调用本工具）", map[string]string{"name": "技能名"}, []string{"name"}),
		Write: false,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			sk, ok := skills.Lookup(name)
			if !ok {
				return "", fmt.Errorf("skill.view: 技能 %q 不存在", name)
			}
			body := strings.TrimSpace(sk.Body)
			if len(body) > toolResultBudget {
				body = body[:toolResultBudget] + " …(截断)"
			}
			return "<skill name=\"" + sk.Name + "\">\n" + body + "\n</skill>", nil
		},
	})
}

// RegisterBuiltinTools 把 Session 状态（记忆/任务）与技能目录注册成模型可调工具。
// 返回注册表（调用方可用 Register 继续加自己的工具，如 os.view / kernel.submit）。
//
// 写工具（mem.set / mem.reset / tasks.add / tasks.set_state）全部标记 Write=true：
// 模型调用它们需要会话白名单，否则 fail-closed 拒绝 —— 这正是「AI 只能提议」
// 纪律的工具级落地。
func (s *Session) RegisterBuiltinTools(skills *SkillsDir) error {
	// delegate 仅在根会话（nesting==0）注册；子会话不带它 → 结构上禁止无限委派。
	if s.nesting == 0 {
		if err := s.registerDelegateTool(); err != nil {
			return err
		}
	}
	if err := registerMemoryTools(s.registry, s); err != nil {
		return err
	}
	if err := registerTaskTools(s.registry, s); err != nil {
		return err
	}
	if err := registerFSTools(s.registry, s); err != nil {
		return err
	}
	return registerSkillView(s.registry, skills)
}
