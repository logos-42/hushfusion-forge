# ProjectionPhysics 锚点 provenance 刷新报告

- 日期：2026-10-25
- 执行：小龙
- 提交：`6f1be184b984bf11dd9bfa2a57110d38d5d85962`
- 文件：`testdata/projectionphysics_anchors.json`（1 file changed, 5 insertions(+), 5 deletions(-)）

## 背景
上游 ProjectionPhysics HEAD 由 cc95cc6a 推进到 2fa3b7de（feat(mu-control): μ 反馈控制律闭环）。
`emit_pp_anchors.py --check` 报 provenance 漂移；数值锚点逐字段 diff 无变化。

## 变更内容（仅 provenance，5 处）
| 字段 | 旧值 | 新值 |
|---|---|---|
| upstream_commit | cc95cc6a2435...ca6ca8 | 2fa3b7de71ef...a7a2e |
| upstream_commit_subject | docs(paper3): ... (v1.4) | feat(mu-control): μ 反馈控制律闭环（MAC7-10/C1-7）+ 最小物理实验设计（D1 最小化） |
| generated_at_utc | 2026-10-17T15:28:56Z | 2026-10-25T04:25:00Z |
| generated_at_local | 2026-10-17T23:28:56.568884+08:00 | 2026-10-25T12:25:00.144354+08:00 |
| source_of_truth | 含 cc95cc6a... | 含 2fa3b7de...（其余文字不变） |

## 数值锚点零漂移证据
- 场源材料类 7 行：不变
- B_death 扫描 8 个 a：不变
- moirefield 诚实边界 5 条：不变
- fusionroadmap 诚实边界 4 条：不变
- 关闭步：上游整数字段 165 / 解析复算 164.241124（η=0.05）：不变

## 验证
1. `python3 scripts/emit_pp_anchors.py` 重发成功（read_from 7 个 artifact，skipped_fields 空）
2. `python3 scripts/emit_pp_anchors.py --check` → PASS（仅忽略 generated_at_* 与 upstream_root）
3. `go test ./internal/design/` → ok（0.015s）

## 交叉印证
小宝独立重发（/tmp/pp_anchors_new.json）结论一致：5 处差异全在 provenance，数值零漂移。

## 后续
上游再推 commit→ 按相同流程刷新 provenance（不发新任务，流程已锁定）。
