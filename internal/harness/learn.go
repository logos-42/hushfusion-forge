package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Sample 是喂给持续学习器的一条 (观测, 动作, 奖励) 样本。奖励取 score 增量，
// 因为自调整的目标是"选增量更高的动作"。
type Sample struct {
	Obs    []float64 `json:"obs"`
	Action int       `json:"action"`
	Reward float64   `json:"reward"`
}

// ContinualLearner 是"自我调整"第二层（持续学习）的干净接口。
//
// 当前默认实现是 Python 子进程（复用 scripts/oml_continual_core.py）；该接口之后
// 可平滑换成 Go 原生实现，无需改动 Harness 的其余部分 —— 这就是"留 Go 接口"。
type ContinualLearner interface {
	// Fit 在样本上训练/增量更新模型。错误表示后端不可用（应 fail-closed 回退）。
	Fit(samples []Sample) error
	// Act 返回给定观测下的偏好动作下标。
	Act(obs []float64) (int, error)
}

// pythonLearner 通过子进程调用 scripts/harness_oml.py，用 JSON over stdin/stdout
// 通信。脚本内部复用 oml_continual_core.OMLDesignLearner。
//
// 纪律：torch / hibs_lnn 缺失时脚本以非 0 退出，这里把错误透传给调用方，
// 由 Harness 决定回退到规则层 —— 绝不假装学会。
type pythonLearner struct {
	script    string
	model     string
	obsDim    int
	actionDim int
}

// runJSON 把 in 序列化为 JSON 喂给脚本，可选地解析 out。
func (p *pythonLearner) runJSON(in any, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("harness oml: marshal request: %w", err)
	}
	cmd := exec.Command("python3", p.script)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("harness oml: invoke %s: %w: %s", p.script, err, trunc(stderr.String(), 500))
	}
	if out != nil {
		if err := json.Unmarshal(stdout, out); err != nil {
			return fmt.Errorf("harness oml: parse response: %w (stdout %q)", err, trunc(string(stdout), 300))
		}
	}
	return nil
}

func (p *pythonLearner) Fit(samples []Sample) error {
	return p.runJSON(map[string]any{
		"cmd":       "fit",
		"model":     p.model,
		"obs_dim":   p.obsDim,
		"n_actions": p.actionDim,
		"samples":   samples,
	}, nil)
}

func (p *pythonLearner) Act(obs []float64) (int, error) {
	var res struct {
		Action int `json:"action"`
	}
	if err := p.runJSON(map[string]any{
		"cmd":       "act",
		"model":     p.model,
		"obs":       obs,
		"obs_dim":   p.obsDim,
		"n_actions": p.actionDim,
	}, &res); err != nil {
		return 0, err
	}
	return res.Action, nil
}

// trunc 截断字符串便于报错信息展示。
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
