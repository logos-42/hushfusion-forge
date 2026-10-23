package main

// tools_fusion.go —— 设施 / 仿真能力：设计打分、搜索、基线、registry 前 k、装置状态。
// 诚实边界：这里回答的是真空静磁学与占位装置链路，不回答“这台装置能不能聚变”。

import (
	"context"
	"fmt"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/app"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/plant"
	"github.com/logos-42/hushfusion-forge/internal/runner"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// evalScorer 把 Evaluator 接到 search 的 Scorer 接缝（不写 registry）。
type evalScorer struct{ ev *objective.Evaluator }

func (e evalScorer) Score(x []float64, _ runner.Meta) objective.EvalResult { return e.ev.Evaluate(x) }

func registerFusionTools(s *agent.Session, base string) {
	spec := app.DefaultSpec()
	ev := app.AnalyticEvaluator(spec, 1.0)
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("fusion.evaluate",
			"给一组线圈设计打分（真空静磁学；不回答能否聚变）",
			map[string]any{
				"design": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "number"},
					"description": "参数向量 [r,z,I] × N，长度必须等于 spec.NParams()",
				},
			}, []string{"design"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			x, err := argFloats(args, "design")
			if err != nil {
				return "", err
			}
			if len(x) != spec.NParams() {
				return "", fmt.Errorf("fusion.evaluate: design 长度 %d，spec 需要 %d", len(x), spec.NParams())
			}
			if _, err := physics.VectorToCoils(x, spec); err != nil {
				return "", err
			}
			return jsonText(ev.Evaluate(x))
		},
	})

	register(agent.Tool{
		Spec: projectTool("fusion.search",
			"在预算内搜索线圈设计（random / evolution / evolution-knowledge 等）",
			map[string]any{
				"method": map[string]any{"type": "string", "description": "搜索方法；缺省 evolution"},
				"budget": map[string]any{"type": "integer", "description": "评估次数，缺省 20，上限 200"},
				"seed":   map[string]any{"type": "integer", "description": "随机种子"},
			}, nil),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			method := argString(args, "method", "evolution")
			budget := argInt(args, "budget", 20)
			if budget < 1 {
				budget = 1
			}
			if budget > 200 {
				budget = 200
			}
			seed := argInt(args, "seed", 0)
			opt := app.RunOptions(spec, method, seed, budget, 1, 0, nil, 0, 0, 0)
			res, err := search.Run(method, evalScorer{ev}, opt)
			if err != nil {
				return "", err
			}
			return jsonText(res)
		},
	})

	register(agent.Tool{
		Spec: projectTool("fusion.baseline", "返回教材基线线圈设计与评分", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			b, err := app.BaselineDesign(spec)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{"baseline": b, "evaluation": ev.Evaluate(b.Design)})
		},
	})

	register(agent.Tool{
		Spec: projectTool("fusion.top",
			"从 registry 里取评分最高的可行设计",
			map[string]any{
				"k": map[string]any{"type": "integer", "description": "取前 k 个，缺省 5"},
			}, nil),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			path, err := app.FindFile("runs/scratch/registry.jsonl")
			if err != nil {
				return "", fmt.Errorf("fusion.top: 找不到 registry: %w", err)
			}
			reg, err := app.OpenExistingRegistry(path)
			if err != nil {
				return "", err
			}
			k := argInt(args, "k", 5)
			if k < 1 {
				k = 1
			}
			xs, err := app.TopDesigns(reg, spec, k)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{"registry": path, "designs": xs})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plant.state", "读取仿真装置当前状态（占位模型，不是物理）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			dev := plant.New(defaultPlantSpec(), time.Now())
			return jsonText(dev.State())
		},
	})
}

func defaultPlantSpec() plant.Spec {
	return plant.Spec{
		DeviceID: "forge-sim",
		Actuators: []plant.ActuatorSpec{
			{Name: "heating", Initial: 0, Gain: 1},
			{Name: "coil_current", Initial: 0, Gain: 1},
		},
		Sensors: []plant.SensorSpec{
			{Name: "temperature", Unit: "K", Offset: 300},
			{Name: "field", Unit: "T", Offset: 0},
		},
	}
}
