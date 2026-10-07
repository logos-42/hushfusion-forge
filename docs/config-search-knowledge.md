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

---

# v6 更新: 进化到"知识播种" —— 选定方法(0.986 新纪录)

## 关键突破: evolution_knowledge(知识播种) 稳定 0.986, 打破历史最佳 0.9489
- forge run 加 --knowledge flag(读 registry top-16 播种)
- 守护进程知识库 runs/v5_knowledge/registry.jsonl(初始 phase1 12001 条, 每轮评估累积)
- 只有 evolution_knowledge 用知识播种(其他方法对照), 推荐器学会选它

## 服务器实测(修正特征一致 bug 后 11 轮)
- warm(推荐) 0.813 vs cold(随机) 0.310, Δ=+0.503
- ek 被 warm 选 5/6 次, 全部 0.986 —— **稳定打破历史最佳 0.9489**
- 最近6轮: warm 连续 0.99, cold 随机仅 0.22
- 知识库 12001 → 19901 条(持续生长)

## 真复利闭环(已达到)
```
知识库(registry累积) → evolution_knowledge 播种 → forge 找到 0.986 新纪录
→ 新设计回流知识库 → 下轮播种更强 → 持续校准
```
持续学习模型自主选择: 学会稳定选 ek(知识播种), 不再瞎探索。

## 验证方法
warm/cold 交替对照(偶数轮推荐器, 奇数轮随机), paired 比较。
当前 647 轮 Δ=+0.342 稳定(三段: +0.41/+0.31/+0.31 均正向)。
