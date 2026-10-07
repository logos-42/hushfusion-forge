#!/usr/bin/env python3
"""分形循环 24h 长期持续学习守护进程 —— 反引力控制环境, 固定 vs 分形配对。

leo 要求: 测试「分形生长结构能在长期过程里持续学习吗, 效果如何」,
设置 24h 机制放服务器跑。

设计 (消除探索运气, 直接配对):
  每轮同一 μ₀ 序列喂给两个学习器:
    A. 固定单节点 (PlainNode)
    B. 分形生长树 (FractalNodeV2, 相对衰减→长子树)
  各自 UCB 推荐控制点 → 各自跑 μ 控制 → 各自训练/生长
  记录配对结果 → 24h 后对比: 分形 vs 固定的长期 warm 步数

持续学习机制:
  - 每轮经验回流各自经验池 (滑动窗口, 防爆炸)
  - 分形树: 误差平台(相对衰减)⟹ 长子树 (门控均匀起步 + 熵正则, §2f 修法)
  - ckpt 每 10 轮落盘 (树参数 + 经验池), 重启不冷启动
  - supervisor 自愈 (仿 v5/v6)

用法 (服务器):
  setsid nohup bash scripts/oml_supervisor_fractal.sh > /work/liuyuanjie/oml_supervisor_fractal.out 2>&1 &
  停: touch /work/liuyuanjie/design_daemon_fractal.stop
"""
import argparse
import json
import math
import pathlib
import pickle
import time

import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)

# ---- 反引力 μ 控制环境 (上游 sim_two_flow_ring.py 动力学) ----
ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)
MU_CEIL = 1.0 - ME_OVER_MI      # FC11 天花板 0.999780568
MU_WORK = 0.999                 # 工作窗口
LOG_PENALTY = math.log(5000.0)


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_control(mu0, eta_ext, lam, max_steps=5000):
    mu = mu0
    for n in range(1, max_steps + 1):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu)
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return n, True
        if mu >= MU_WORK:
            return n, False
    return max_steps, False


def ctrl_feature(eta_ext, lam, mu0):
    return [eta_ext / 0.5, lam / 0.9, mu0 / 0.8]


ETA_GRID = np.linspace(0.02, 0.5, 20)
LAM_GRID = np.linspace(0.0, 0.9, 10)


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


# ---- 相对衰减探测器 (生长判据, v3 验证) ----
class RelDecayDetector:
    def __init__(self, thresh=0.25, k=3, alpha=0.5, warmup=4):
        self.thresh, self.k, self.alpha, self.warmup = thresh, k, alpha, warmup
        self.prev = None; self.ewma = 0.0; self.early = []
        self.early_avg = None; self.streak = 0; self.n = 0

    def update(self, val):
        self.n += 1
        if self.prev is None:
            self.prev = val; return False
        imp = (self.prev - val) / abs(self.prev) if self.prev != 0 else 0.0
        if self.n <= self.warmup:
            self.early.append(imp)
            if self.n == self.warmup:
                self.early_avg = max(float(np.mean(self.early)), 1e-9)
            self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
            self.prev = val; return False
        self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
        ratio = self.ewma / self.early_avg
        self.streak = self.streak + 1 if ratio < self.thresh else 0
        self.prev = val
        return self.streak >= self.k


# ---- 固定单节点 ----
class PlainNode:
    def __init__(self, in_dim, d_model=32):
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.opt = torch.optim.Adam(self.net.parameters(), lr=1e-3)

    def predict_flat(self, X):
        with torch.no_grad():
            return self.net(X).flatten().numpy()

    def train_step(self, X, Y, steps=2):
        for _ in range(steps):
            self.opt.zero_grad()
            loss = ((self.net(X) - Y) ** 2).mean()
            loss.backward()
            self.opt.step()

    def state_dict(self):
        return {"net": self.net.state_dict(), "opt": self.opt.state_dict()}

    def load_state_dict(self, sd):
        self.net.load_state_dict(sd["net"])
        self.opt.load_state_dict(sd["opt"])


# ---- 分形生长树节点 (v2 修法: 门控均匀起步 + 熵正则) ----
class FractalNodeV2:
    def __init__(self, in_dim, d_model=32, depth=0, D=4, warmup=8, ent_reg=0.01):
        self.depth, self.D = depth, D
        self.warmup, self.ent_reg = warmup, ent_reg
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.gate = nn.Parameter(torch.zeros(1))
        self.children = []
        self.child_warmup = []
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.update_freq = D ** depth
        self.t = 0
        self.detector = RelDecayDetector()
        self.clean_losses = []

    def n_nodes(self):
        return 1 + sum(c.n_nodes() for c in self.children)

    def depth_of(self):
        if not self.children:
            return self.depth
        return max(c.depth_of() for c in self.children)

    def spawn_child(self, in_dim):
        child = FractalNodeV2(in_dim, d_model=self.net[0].out_features,
                              depth=self.depth + 1, D=self.D,
                              warmup=self.warmup, ent_reg=self.ent_reg)
        self.children.append(child)
        self.child_warmup.append(self.warmup)
        n = len(self.children) + 1
        self.gate = nn.Parameter(torch.full((n,), math.log(n)))
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        return child

    def predict_tree(self, X):
        self_out = self.net(X)
        outs = [self_out]
        child_list = []
        for i, ch in enumerate(self.children):
            if self.child_warmup[i] > 0:
                continue
            cy, _ = ch.predict_tree(X)
            outs.append(cy)
            child_list.append(i)
        if len(outs) == 1:
            return self_out, [self_out]
        gate = torch.softmax(self.gate, dim=0)
        y = torch.zeros_like(self_out)
        y = y + gate[0] * outs[0]
        for k, ch_idx in enumerate(child_list):
            y = y + gate[int(ch_idx) + 1] * outs[k + 1]
        return y, outs

    def predict_flat(self, X):
        y, _ = self.predict_tree(X)
        return y.flatten().detach().numpy()

    def train_step(self, X, Y, steps=2):
        self.t += 1
        for _ in range(steps):
            self.opt.zero_grad()
            y, _ = self.predict_tree(X)
            mse = ((y - Y) ** 2).mean()
            active_children = sum(1 for w in self.child_warmup if w == 0)
            if len(self.children) > 0 and active_children > 0:
                gate = torch.softmax(self.gate, dim=0)
                ent = -(gate * torch.log(gate + 1e-9)).sum()
                loss = mse - self.ent_reg * ent
            else:
                loss = mse
            loss.backward()
            self.opt.step()
        for i in range(len(self.children)):
            if self.child_warmup[i] > 0:
                self.child_warmup[i] -= 1
        self.clean_losses.append(float(mse.detach()))
        self.clean_losses = self.clean_losses[-12:]
        return float(mse.detach())

    def grow_check(self):
        if len(self.clean_losses) < 6 or self.n_nodes() >= 7:
            return False
        return self.detector.update(self.clean_losses[-1])

    def state_dict(self):
        return {
            "net": self.net.state_dict(),
            "gate": self.gate.detach(),
            "opt": self.opt.state_dict(),
            "depth": self.depth,
            "update_freq": self.update_freq,
            "t": self.t,
            "children": [c.state_dict() for c in self.children],
            "child_warmup": self.child_warmup,
            "clean_losses": self.clean_losses,
        }

    def load_state_dict(self, sd, in_dim):
        self.net.load_state_dict(sd["net"])
        with torch.no_grad():
            self.gate = nn.Parameter(sd["gate"])
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.opt.load_state_dict(sd["opt"])
        self.depth = sd["depth"]
        self.update_freq = sd["update_freq"]
        self.t = sd["t"]
        self.children = []
        self.child_warmup = []
        for csd in sd["children"]:
            ch = FractalNodeV2(in_dim, d_model=self.net[0].out_features,
                               depth=csd["depth"], D=self.D,
                               warmup=self.warmup, ent_reg=self.ent_reg)
            ch.load_state_dict(csd, in_dim)
            self.children.append(ch)
        self.child_warmup = sd["child_warmup"]
        self.clean_losses = sd["clean_losses"]


# ---- 守护进程主循环 ----
def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=200000)
    ap.add_argument("--out", default="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_fractal.jsonl")
    ap.add_argument("--ckpt", default="/work/liuyuanjie/forge/artifacts/oml_daemon_fractal_ckpt.pkl")
    ap.add_argument("--interval", type=float, default=1.0)
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon_fractal.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    ckpt = pathlib.Path(args.ckpt)

    rng = np.random.default_rng(int(time.time()) % 10000)
    in_dim = 3

    # 状态: 两个学习器 + 各自滑动经验池 + 累积统计
    plain = PlainNode(in_dim)
    fractal = FractalNodeV2(in_dim, D=4)
    px, py = [], []          # plain 经验池
    fx, fy = [], []          # fractal 经验池
    if ckpt.exists():
        try:
            with open(ckpt, "rb") as f:
                data = pickle.load(f)
            plain.load_state_dict(data["plain"])
            fractal.load_state_dict(data["fractal"], in_dim)
            px, py = data["px"], data["py"]
            fx, fy = data["fx"], data["fy"]
            print(f"[ckpt] 恢复: 分形树 {fractal.n_nodes()} 节点, "
                  f"plain经验 {len(px)}, fractal经验 {len(fx)}", flush=True)
        except Exception as e:
            print(f"[ckpt] 读取失败({e}), 从零开始", flush=True)
            plain = PlainNode(in_dim)
            fractal = FractalNodeV2(in_dim, D=4)
            px, py, fx, fy = [], [], [], []

    # UCB 探索计数 (两者共用同一格点空间)
    N_ETA, N_LAM = 5, 4
    p_explore = np.zeros((N_ETA, N_LAM)) + 1.0
    f_explore = np.zeros((N_ETA, N_LAM)) + 1.0

    def bucket(e, l):
        return min(int(e / 0.5 * N_ETA), N_ETA - 1), min(int(l / 0.9 * N_LAM), N_LAM - 1)

    def pick_ctrl(model, model_x, model_y, explore, rng, mu0, exploit_thresh=40):
        """UCB 推荐; 经验不足时随机。"""
        if len(model_x) < exploit_thresh:
            return random_ctrl(rng)
        cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
        feats = np.array([ctrl_feature(e, l, mu0) for e, l in cands])
        Xt = torch.tensor(feats, dtype=torch.float32)
        pred = model.predict_flat(Xt)
        ucb = np.array([pred[i] - 0.25 * np.log(explore[bucket(e, l)[0], bucket(e, l)[1]])
                        for i, (e, l) in enumerate(cands)])
        idx = int(np.argmin(ucb))
        e_, l_ = cands[idx]
        explore[bucket(e_, l_)] += 1.0
        return e_, l_

    def update_pool(xs, ys, fx_, fy_, cap=400):
        xs.append(fx_); ys.append(float(fy_))
        if len(xs) > cap:
            del xs[: len(xs) - cap]
            del ys[: len(ys) - cap]

    stats = {"plain_warm": [], "fractal_warm": [], "plain_cold": [], "fractal_cold": []}

    for rnd in range(args.rounds):
        if stop_file.exists():
            print("[stop] 退出", flush=True)
            break
        t0 = time.time()
        mu0 = float(rng.uniform(0.01, 0.8))
        is_cold = (rnd % 2 == 1)

        # 两个学习器各推荐/随机
        if is_cold:
            pe, pl = random_ctrl(rng)
            fe, fl = random_ctrl(rng)
        else:
            pe, pl = pick_ctrl(plain, px, py, p_explore, rng, mu0)
            fe, fl = pick_ctrl(fractal, fx, fy, f_explore, rng, mu0)

        # 各跑控制
        pn, po = run_control(mu0, pe, pl)
        fn, fo = run_control(mu0, fe, fl)
        py_ = math.log(pn) if not po else LOG_PENALTY
        fy_ = math.log(fn) if not fo else LOG_PENALTY

        # 经验回流 (滑动窗口)
        update_pool(px, py, ctrl_feature(pe, pl, mu0), py_)
        update_pool(fx, fy, ctrl_feature(fe, fl, mu0), fy_)

        # 训练 (窗口 64)
        if len(px) >= 16:
            Xp = torch.tensor(np.array(px[-64:]), dtype=torch.float32)
            Yp = torch.tensor(np.array(py[-64:]), dtype=torch.float32).reshape(-1, 1)
            plain.train_step(Xp, Yp)
        if len(fx) >= 16:
            Xf = torch.tensor(np.array(fx[-64:]), dtype=torch.float32)
            Yf = torch.tensor(np.array(fy[-64:]), dtype=torch.float32).reshape(-1, 1)
            floss = fractal.train_step(Xf, Yf)
            # 生长: 误差平台 ⟹ 长子树
            if fractal.grow_check():
                nb = fractal.n_nodes()
                fractal.spawn_child(in_dim)
                print(f"[生长] round={rnd} 树 {nb}→{fractal.n_nodes()} 节点, "
                      f"深 {fractal.depth_of()}", flush=True)
                fractal.detector = RelDecayDetector()

        # 统计
        if is_cold:
            stats["plain_cold"].append(pn)
            stats["fractal_cold"].append(fn)
        else:
            stats["plain_warm"].append(pn)
            stats["fractal_warm"].append(fn)

        # 趋势行
        row = {
            "round": rnd, "is_cold": is_cold, "mu0": round(mu0, 3),
            "plain_steps": pn, "fractal_steps": fn,
            "plain_over": po, "fractal_over": fo,
            "fractal_nodes": fractal.n_nodes(),
            "ts": time.strftime("%Y-%m-%dT%H:%M:%S"),
        }
        with open(out, "a") as f:
            f.write(json.dumps(row) + "\n")

        # ckpt 每 10 轮
        if rnd % 10 == 0:
            with open(ckpt, "wb") as f:
                pickle.dump({"plain": plain.state_dict(),
                             "fractal": fractal.state_dict(),
                             "px": px[-200:], "py": py[-200:],
                             "fx": fx[-200:], "fy": fy[-200:]}, f)

        # 周期报告
        if rnd % 100 == 0 and stats["plain_warm"]:
            pw = stats["plain_warm"][-100:]
            fw = stats["fractal_warm"][-100:]
            print(f"[round {rnd}] warm 均值: 固定 {np.mean(pw):.0f} 步 | "
                  f"分形 {np.mean(fw):.0f} 步 | 树 {fractal.n_nodes()} 节点 "
                  f"[{time.time()-t0:.2f}s]", flush=True)
        time.sleep(args.interval)

    if stats["plain_warm"]:
        pw, fw = stats["plain_warm"], stats["fractal_warm"]
        print(f"\n=== 分形长期守护进程结束: {len(pw)} warm 轮 ===", flush=True)
        print(f"固定 warm 均值 {np.mean(pw):.0f} 步 vs 分形 {np.mean(fw):.0f} 步", flush=True)
        print(f"分形树最终 {fractal.n_nodes()} 节点, 深 {fractal.depth_of()}", flush=True)


if __name__ == "__main__":
    raise SystemExit(main())