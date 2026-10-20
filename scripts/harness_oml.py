#!/usr/bin/env python3
"""HUSHFUSION harness 的 OML 子进程适配器。

复用 scripts/oml_continual_core.py 的 OMLDesignLearner，把"自我调整"第二层
（持续学习）以 JSON over stdin/stdout 暴露给 Go 的 internal/harness：

    {"cmd":"fit","model":PATH,"obs_dim":N,"n_actions":K,"samples":[{"obs":[...],"action":a,"reward":r}]}
    {"cmd":"act","model":PATH,"obs":[...],"obs_dim":N,"n_actions":K}   ->  {"action":a}

建模约定：把 (obs, action) 拼成输入 obs + onehot(action)（维数 N+K），
回归"该 (观测,动作) 的 score 增量"。act 时对每个候选动作前向一遍，取预测增量最大者。

后端依赖 torch 与 hibs_lnn（oml_continual_core 内部使用）。缺失时以非 0 退出，
由 Go 侧 fail-closed 回退到规则层 —— 绝不假装学会。
"""
import json
import sys
import os

# 允许从仓库根目录直接调用：把 scripts/ 加入 sys.path 以便 import oml_continual_core。
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
if SCRIPT_DIR not in sys.path:
    sys.path.insert(0, SCRIPT_DIR)


def _load_backend():
    """返回 (OMLDesignLearner, torch, np)；任一不可用时抛 ImportError。"""
    import numpy as np
    import torch
    from oml_continual_core import OMLDesignLearner
    return OMLDesignLearner, torch, np


def onehot(action, n_actions):
    v = [0.0] * n_actions
    if 0 <= action < n_actions:
        v[action] = 1.0
    return v


def do_fit(req):
    OMLDesignLearner, torch, np = _load_backend()
    obs_dim = int(req["obs_dim"])
    n_actions = int(req["n_actions"])
    in_dim = obs_dim + n_actions
    model_path = req["model"]
    samples = req.get("samples", [])

    learner = None
    if os.path.exists(model_path):
        try:
            learner = OMLDesignLearner(n_features=in_dim)
            learner.load(model_path)
        except Exception:
            learner = None
    if learner is None:
        learner = OMLDesignLearner(n_features=in_dim)

    os.makedirs(os.path.dirname(os.path.abspath(model_path)), exist_ok=True)
    for s in samples:
        obs = list(s["obs"])
        act = int(s["action"])
        reward = float(s["reward"])
        design = obs + onehot(act, n_actions)
        learner.add_sample(design, reward)

    learner.train_one()
    learner.save(model_path)
    return {"ok": True, "trained": len(samples)}


def do_act(req):
    OMLDesignLearner, torch, np = _load_backend()
    obs_dim = int(req["obs_dim"])
    n_actions = int(req["n_actions"])
    in_dim = obs_dim + n_actions
    model_path = req["model"]
    obs = list(req["obs"])

    if not os.path.exists(model_path):
        # 模型不存在：交给 Go 侧随机兜底。
        return {"action": -1, "error": "model_not_found"}

    learner = OMLDesignLearner(n_features=in_dim)
    learner.load(model_path)

    best_a, best_r = -1, float("-inf")
    with torch.no_grad():
        for a in range(n_actions):
            design = np.array(obs + onehot(a, n_actions), dtype=np.float32)
            r = float(learner.predict(design)[0])
            if r > best_r:
                best_r, best_a = r, a
    if best_a < 0:
        return {"action": -1, "error": "no_candidate"}
    return {"action": best_a}


def main():
    raw = sys.stdin.read().strip()
    if not raw:
        print(json.dumps({"error": "empty request"}), file=sys.stderr)
        sys.exit(2)
    try:
        req = json.loads(raw)
    except json.JSONDecodeError as e:
        print(json.dumps({"error": f"bad json: {e}"}), file=sys.stderr)
        sys.exit(2)

    try:
        if req.get("cmd") == "fit":
            res = do_fit(req)
        elif req.get("cmd") == "act":
            res = do_act(req)
        else:
            res = {"error": f"unknown cmd {req.get('cmd')}"}
    except Exception as e:  # ImportError / torch 缺失 / 其它后端错误
        print(json.dumps({"error": f"{type(e).__name__}: {e}"}), file=sys.stderr)
        sys.exit(3)

    print(json.dumps(res))
    sys.exit(0)


if __name__ == "__main__":
    main()
