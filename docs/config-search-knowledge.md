# forge 搜索配置规律 —— 持续学习模型学到的知识 (v5-UCB, 647轮)

来源: design_daemon_config_v5.py (OML 配置推荐器) 在服务器 647 轮 warm/cold 对照,
warm(推荐) 0.578 vs cold(随机) 0.236, Δ=+0.342, 255/321 wins 稳定优势。

## 规律 1: budget 越大越好(单调强效应)
| budget | warm 均值 | 说明 |
|--------|-----------|------|
| 50     | +0.097    | 太小, 搜不出好设计 |
| 100    | +0.147    | 仍弱 |
| 200    | +0.515    | 明显改善 |
| 400    | +0.698    | 最优档 |

⟹ 配置推荐时优先 budget=400(当前配置空间上限)。

## 规律 2: evolution 系 + 大 budget + 小 mu/lam 最高效
warm 高频且高分(top10):
- evolution/400/mu8/lam24:   0.822 (n=17)
- evolution_warm/400/mu8/lam24: 0.806 (n=12)
- evolution/400/mu8/lam48:   0.790 (n=15)
- evolution_warm/400/mu8/lam96: 0.745 (n=8)
- evolution/400/mu16/lam24:  0.717 (n=14)

⟹ mu=8(小父代)+lam=24(小子代) 配 budget=400 是稳定高效组合。
大 mu/lam 反而差(探索过多/收敛慢)。

## 规律 3: 方法对比(同 budget 下)
- evolution / evolution_warm: 最优(搜索能力强)
- random: 中等
- lhs: 较弱(分层采样在12维空间优势不大)

⟹ 优先 evolution 系, warm(人类baseline播种)略优于冷。

## 可操作结论(给 forge 配置)
默认推荐: evolution_warm / budget=400 / mu=8 / lam=24
备选:      evolution / budget=400 / mu=8 / lam=48

## 验证方法
warm/cold 交替对照(偶数轮推荐器, 奇数轮随机), paired 比较。
当前 647 轮 Δ=+0.342 稳定(三段: +0.41/+0.31/+0.31 均正向)。
