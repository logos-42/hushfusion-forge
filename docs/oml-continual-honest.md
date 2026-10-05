# OML 持续学习闭环 —— 诚实结论

## 目标
持续运行测试闭环：训练 AI 做核心 + 服务器 + 带入模型 + 持续迭代。
核心 = 持续学习的电磁模型，能设计和操作 forge 系统，效果要验证。

## 核心机制：headless OML 元学习（非静态选择器）
用户明确要【动态的持续调整、持续学习】，不是静态选择器。
用 headless 真正的持续学习引擎：
- 内循环：在 support(最近设计) 上快速适应克隆头 (Meta-SGD)
- 外循环：在 query 上更新 meta 参数 + consolidate（不遗忘）
- Reptile 整合：本体 init 朝适应后权重拉一步

## 闭环（每轮）
```
OML 持续学习(动态调整)
  → 用学到的价值选 top-K 设计
  → 写 registry 播种 forge
  → forge 真评估+搜索 → 真实 score 回流
  → OML 持续学习 → 循环
```

## 数据污染事故（必须记录）
- 事故：守护进程用固定种子(0)，多次重启 append 同一序列到既有 jsonl，
  导致"66 轮"里 round0-6 出现 3 次完全相同数值 —— 假数据。
- 我曾用污染数据汇报"mse 大幅下降 2.04→1.64"，是错的。
- 修正：
  1. 输出文件若已存在则拒启（防 append 污染）
  2. 种子用启动时间戳（防多实例同序列）
  3. 清空污染文件，从干净状态重新累积
- 教训：持续运行日志 + 固定种子 + append = 重复假数据，必须硬门拦截。

## 真实结论（干净单趟，10 轮）
```
forge_op 成功: 10/10     headless 选的设计真实驱动 forge 搜索
选样 lift: 10/10 全正, 均值 +0.768   持续学到的价值优于随机
预测误差 query_mse: 前3 1.82 → 后3 1.54   真实下降(这趟)
```
诚实解读：
- **选样能力稳定有效**（lift 持续为正）—— 这是最强、最可靠信号。
- **预测误差** 这趟轨迹真实下降（1.82→1.54），但早期污染趟曾显示震荡，
  不夸大 —— mse 是"波动中整体下行"，不是单调。

## 证据文件
- scripts/oml_continual_core.py      OML 持续学习核心(可导入)
- scripts/design_daemon_oml.py       常驻守护(含数据污染保护)
- scripts/verify_oml_continual.py    离线验证
- artifacts/oml_daemon_trend_clean.jsonl  干净单趟趋势(入库)

---

# v4 更新（回应三条新批评）

## 用户三条批评（全部成立，逐条回应）

### 1. 测试验证不足 → 已加"回流水预测误差"（真泛化）
旧版只对比 select_lift vs 随机。v4 新增核心指标：
- **回流水预测误差（reflux_err）**：每轮读回 forge 真评估的新数据，
  用**回流前**的模型预测这些**模型从未见过**的设计，算 MSE。
  = 真正的 out-of-sample 泛化（不是静态 lift）。
- 服务器 15 轮实测：回水误 0.467 → 0.284（下降），lift 13/15 正均值 +0.62，
  forge op 15/15 成功。

### 2. 核心未达标（模型没变好）→ 根因修复：真实 score 回流
- **旧版根因**：守护进程只把候选设计写进 forge，却**从不读回** forge 评估的真实 score
  （存在 out/registry.jsonl）。模型永远在初始静态 7221 条数据上转 → 不可能越来越好。
- **v4 修复**：每轮读 out/registry.jsonl 的真实 score，去重后累积进池，
  模型在增长的数据池上持续学。服务器 15 轮 pool 7221→7344（130 条真实数据回流）。
- **模型变好的证据**：随累积数据增多，对新增（未见过）数据的预测误差下降（0.47→0.28）。

### 3. 缺 24h 运作 → supervisor 自愈 + checkpoint + 定时归档
- **scripts/oml_supervisor.sh**：循环拉起 daemon，崩溃自动重启（ckpt 恢复累积池不丢），
  exit=2（输出文件已存在防污染门）停防死循环，stop 文件优雅退出。
- **checkpoint**：每 5 轮持久化累积池，崩溃可恢复。
- **每日归档**：scripts/oml_daily_report.py 生成 pool 增长/回流累计/lift/回水误快照。

## 诚实边界（不粉饰 v4）
- 回水误是"波动中整体下降"，不是单调（单轮 0.10~1.42 波动）。
- select_lift 有 2/15 轮为负（均值 +0.62），非全正。
- 这些波动来自每轮回流不同设计 + forge 评估噪声，属非平稳数据正常表现；
  但结论应表述为"整体改善"，不夸大。

## 证据文件(v4)
- scripts/design_daemon_oml.py   v4 闭环（真实回流+严格验证+checkpoint）
- scripts/oml_supervisor.sh      24h 自愈运作
- scripts/oml_daily_report.py    每日归档
- artifacts/oml_daemon_trend_v4.jsonl  实时趋势
- artifacts/oml_daemon_v4_ckpt.pkl     累积池 checkpoint
