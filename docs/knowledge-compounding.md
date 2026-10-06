# 持续学习复利本质 —— 知识播种 > 配置优化（关键实验结论）

日期: 2026-10-06 (loop 迭代, 服务器实测)

## 核心发现
用 forge 真搜索对照三种搜索方式(等 budget=400):

| 方式 | 种子 | best_score | 说明 |
|------|------|-----------|------|
| evolution_warm (mu8/lam24) | 人类 baseline | 0.880/0.648/0.769 (均值0.766) | 配置推荐器优化的单次搜索 |
| evolution_knowledge (benchmark) | registry 知识 (12001条) | **0.9489** (seed 0/1 一致) | 知识播种, 命中历史最佳 |
| evolution_knowledge (forge run) | 无知识种子 | 0.768 | 没读知识库=退化为普通搜索 |

## 结论: 复利在"知识累积", 不在"每轮配置"
- 配置推荐器(UCB) 652+ 轮 warm 0.576 vs cold 0.237 (Δ+0.34) —— 过程收益真实但有限(0.77-0.89)
- **evolution_knowledge 用 registry 累积的设计知识播种 = 0.9489, 直接命中历史最佳, 且跨 seed 稳定**
- ⟹ 持续学习的真正复利 = **registry 里累积的 12001 条设计知识本身**
  (每条设计是 forge 用评估成本换来的知识, 播种即复用)

## 对"设置后续聚变磁场理解能力"的含义
1. 持续学习模型能自主选高效配置(UCB, Δ+0.34) —— 已达成
2. **但更强的杠杆是知识积累**: 让 registry 持续生长, 新设计用 evolution_knowledge
   播种已有知识, 每轮搜索都站在之前 12001 条设计的肩膀上
3. 建议方向: v5 守护进程从 evolution_warm 升级为 evolution_knowledge(读 registry 播种),
   每轮搜索产出回流 registry → 知识库增长 → 下轮播种更强 = 真·持续校准

## 证据
- benchmark evolution_knowledge --knowledge runs/phase1: best=0.9488836640716174 (seed0,1)
- config推荐器 warm/cold: 652轮 Δ=+0.339 (artifacts/oml_daemon_trend_v5_643.jsonl)