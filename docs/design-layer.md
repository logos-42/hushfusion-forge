# 内部设计判决层（design layer）— 对齐 ProjectionPhysics 的计算图纸

## 0. 为什么有这一层

Forge v0.1 的评分函数只问「中平面的场均不均匀」。那**不是**装置设计问题：它让一个把线圈贴到
距打分点 **4.196 mm** 的退化解拿了第一名（D9857，`runs/phase0/`）。真正决定装置成败的门在
ProjectionPhysics 的内部设计里——场能不能撑住设计密度（PF3/FC1）、μ 窗口开不开（FC11b）、
被约束的步数预算有多少（TD19–TD21）。本层把这些门逐条实现成**可计算、可回归**的判据。

词条口径**一律沿用上游仓库**，不在这里重新发明物理，也不把排程假设当物理。

## 1. 上游出处（唯一真源）

> 上游 = **`logos-42/Hibs-Physics`**（本地工作副本 `/Users/apple/Downloads/lean/ProjectionPhysics`，读出时 commit `d28cd417b3f4`）。
> 注意：上游 `artifacts/` **被 .gitignore 忽略、不在 git 树里** —— 锚点的真正出处是「本地重跑上游脚本的产物」，
> 可用 `python3 scripts/verify_moire_field.py` 重放（实测重放后 `report.json` 逐字节相同，唯一差异是 `meta.date`）。
> 本仓库不许把这件事写成「来自上游仓库提交的 artifacts」；锚点文件里记的是客观事实
> （上游 commit / 实际读到的文件 / `upstream_artifacts_tracked_by_git: 0`）。

| 内容 | 上游文件 |
|:--|:--|
| 公式本体（B_min / n_max / X_req / B_death / χ_μ / p_rel / v_rel） | `scripts/verify_moire_field.py` §常数 + `def n_max/B_min/n_op/tau_lawson/X_req/B_death/p_rel/v_rel/chi_mu` |
| 锚点数字（七层账本 M1–M8） | `ProjectionPhysics/artifacts/moirefield/{report.json,summary.txt}` |
| 工程参数表（ρ_i、τ_E、μ 工作区间、FC5） | `ProjectionPhysics/docs/wiki/theory-antigravity-confinement.md` §4/§6 |
| μ 动力学（TD19–TD21：窗口余量 / 锁定因子 / 关闭步） | `ProjectionPhysics/artifacts/mudynamics/summary.txt` |
| 三个判决量 D1/D2/D3 | `ProjectionPhysics/artifacts/fusionroadmap/summary.txt` |

## 2. 公式（逐字移植，不得改写、不得「顺手优化」）

```text
常数（与上游同值同精度）
  MU0 = 4π×1e-7 H/m      KB = 1.380649e-23 J/K     EV = 1.602176634e-19 J
  ME  = 9.1093837015e-31 kg   U = 1.66053906660e-27 kg   MI = 2.5·U (D-T)
  T_KEV = 15.0  ⟹ KT_J = T_KEV·1e3·EV
  BETA = 1.0     N_DESIGN = 1e20 m⁻³     A_REF = 0.2 m
  D0 = 1.11 m²/s (ITER 级 μ=0 标定)  ⟹ TAU0(a) = a²/D0
  NT_LAWSON = 2e20 (m⁻³·s)      FLOOR = ME/MI = 2.1943e-4 (FC11 地板)

场-密度（PF3 / FC1）
  n_max(B)      = BETA·B²/(4·MU0·KT_J)          [FC1]
  B_min(n)      = √(4·MU0·KT_J·n/BETA)          [PF3 反解]
  n_op(B)       = min(N_DESIGN, n_max(B))
  τ_L(n)        = NT_LAWSON/n                    [劳森要求的最小约束时间]

μ 窗口（FC4 / FC11b）
  X_req(B,a)    = (TAU0(a)/τ_L(n_op(B)))²        [τ_E = τ₀/√(1−μ) ≥ τ_L ⟹ 1−μ ≤ (τ₀/τ_L)²]
  B_death(a)    : 解 X_req = FLOOR ⟹ n_cross = (NT_LAWSON/TAU0(a))·√FLOOR
                  B_death = √(4·MU0·KT_J·n_cross/BETA)   （∝ 1/a）
  χ_μ(B,a)      = X_req(B,a)/FLOOR               （> 1 ⟹ 可行；< 1 ⟹ μ 窗口关闭 = 无解，不是「更难」）

功率 / 体积（相对 B_ref = 9 T 满 β 基准）
  p_rel(B)      = (n_max(B)/n_max(9))²           [P ∝ B⁴]
  v_rel(B)      = 1/p_rel(B)

μ 动力学（TD19–TD21）
  状态方程      μ_{n+1} = μ_n + η(1−μ_n)
  闭式解        μ_n = 1 − (1−η)^n·(1−μ₀)
  窗口余量      m_i(1−μ_n)：沿轨道严格递减（TD19），不自行恢复
  锁定因子      1/√(1−μ_n)：分母恒正（TD8）且单调递增（TD20）
  关闭步判据    (1−η)^n·(1−μ₀) ≤ m_e/m_i      [TD21]
                ⟹ n_close = ln((1−μ₀)/(m_e/m_i)) / ln(1/(1−η))，η<1；η≥1 ⟹ +∞（一步到 1，无窗口）
  由增益反解 μ  μ = 1 − 1/g²（τ_E 增益 g；g=10 ⟹ μ=0.99）[FC4 反解]

判决量（定义即口径，本层只报「怎么测」，不做预测）
  D1  R_ci(μ)   = 1/(1−μ) − 1                  [FC9]
      μ_min(δ)  = δ/(1+δ)                      （诊断相对精度 δ）
  D2  Λ         = τ_E 增益 / S* 惩罚 —— FC5 预言 ≡ 1；几何捕获能否 >1 **未证** ⟹ 本层报 unknown
  D3  k         = dlnμ/dlnP；4 个数量级所需功率倍数 = (1e4)^(1/k)
```

## 3. 锚点（必须逐位复现，容差写明）

| 量 | 上游值 | 来源 |
|:--|:--|:--|
| `B_min(N_DESIGN)` | 1.0991 T（断言容差 5e-3） | `verify_all.py` MF-M2 |
| `B_death(0.2)` | 0.9966 T（容差 3e-3） | MF-M5★ |
| `B_death` 缩放 | `B_death(0.1)/B_death(0.2) = 2.0`（2%）、`0.05 → 4.0`（3%） | MF-M5 |
| `χ_μ(1.60, 0.2)` | 1.479（断言 1.476±0.03） | MF-M4 |
| `χ_μ(0.12, 0.2)` | 2.102e-4（`< 1e-3`） | MF-M4 |
| `n_max(1.6)` | 2.119e20 m⁻³ | M8（由 FC1 复算） |
| `n_op(0.12)` | 1.192e18 m⁻³ | M8 |
| `p_rel/v_rel(9.0)` | 1.0 / 1.0（基准自洽） | M3 |
| `TAU0(0.2)` | 0.0360 s | summary.txt |
| `n_close(η=0.05, μ₀=0, D-T)` | **165 步** | `mudynamics/summary.txt` N11 |
| `μ` 天花板 | `1 − m_e/m_i = 0.999781` | FC11（D-T） |
| `k=1 / 0.5 / 0.25 / 0.1` 的功率倍数 | 1e4 / 1e8 / 1e16 / 1e40 | fusionroadmap summary |

**锚点必须来自上游 artifacts 的真实文件**：`scripts/emit_pp_anchors.py` 读
`/Users/apple/Downloads/lean/ProjectionPhysics/artifacts/...` 写成
`testdata/projectionphysics_anchors.json`（含上游 commit + 读取时间），Go 测试逐条比对。
**不许**把锚点直接抄进 Go 源码当常量——那样就变成自证。

## 4. API（冻结）

```go
package design

// Input 是一次设计判决的外部条件（不是设计变量）。
type Input struct {
    DeviceScaleM float64 // a [m]：等离子体/约束区尺度（上游 A_REF=0.2）
    SourceKey    string  // 场源材料类（见 Sources），决定 B_cap
    Eta          float64 // μ 状态方程增益 η ∈ [0,2)
    Mu0          float64 // 初始 μ
}

// Gate 是一条判据的结果：可算、可回归、带出处。
type Gate struct {
    Key       string  // "field_min" / "death" / "mu_window" / "coil_load" / "buildable" / "steps"
    Label     string  // 中文短名
    Formula   string  // 实算用的公式（照 §2 抄）
    Value     float64 // 实算值
    Ref       float64 // 判据阈值（0 表示无阈值）
    Pass      bool    // 判定
    Unknown   bool    // 未证/不可判（如 FC5 锁定）→ 不得计入 pass
    Provenance string // 上游条目：FC1/PF3/FC11b/FC4/TD21/...
}

// Scope 是一份设计判决层的输出：输入 + 上游材料行 + 逐条门 + 总结论。
type Scope struct { ... }

// Review 对一份 coil 设计（由 spec + metrics 给定）跑全部门。
func Review(spec config.Spec, m physics.Metrics, in Input) Scope

// 闭式解（上游逐字移植，导出以便单独锚定）
func NMax(b float64) float64
func BMin(n, beta float64) float64
func NOp(b float64) float64
func TauLawson(n float64) float64
func XReq(b, a float64) float64
func BDeath(a float64) float64
func ChiMu(b, a float64) float64
func PRel(b, bRef float64) float64
func VRel(b, bRef float64) float64
func MuAfterSteps(mu0, eta float64, n int) float64
func WindowCloseStep(mu0, eta, meOverMi float64) float64
func LockingFactor(mu float64) float64
func MuFromGain(g float64) float64
func Rci(mu float64) float64
func MuMinFromDelta(delta float64) float64
func PowerMultiple(k, decades float64) float64

// Sources / Source 是场源材料类表（7 行，含文献出处）。
func SourceTable() []Source
func LookupSource(key string) (Source, bool)
```

## 5. 门（六条）

| Key | 判据 | 失败含义 |
|:--|:--|:--|
| `field_min` | 约束场 `B_throat` ≥ `B_min(N_DESIGN)` | β>1：装置撑不住设计密度 |
| `death` | 该场源 `B_cap` ≥ `B_death(a)` | **无解**（μ 窗口关闭），不是「更难」 |
| `mu_window` | `χ_μ(B_cap, a) > 1` | 该场源在 a 下够不到 μ 工作点 |
| `coil_load` | `B_coil_max` ≤ min(`spec.CoilFieldLimit`, `B_cap`) | 导体场超天花板（HTS 极限/材料极限） |
| `buildable` | `min_coil_gap ≥ 2·spec.TPack` 且 `!coil_proximity_floor_hit` | 几何不可造（D9857 那类退化解在这里死） |
| `steps` | `n_close > 0`（η<1）——报值，不判生死 | η≥1 ⟹ 一步到 1，窗口不存在 |

**FC5 锁定**单独报 `Unknown: true`（上游未证，不得报 pass）。

## 6. 诚实边界

- **μ 的主动产生 = 第二输入缺口**：本层**不算** μ，只算「给定 μ 能维持多少步、需要多大的场」。
  上游原文：缺口从「η 是哪来的」变成「抹平功率是哪来的」。
  **2026-10-01 上游把这条缺口定位得更准了**（上游 `docs/wiki/theory-scale-gap-anatomy.md`）：
  它不是「少一个数」，而是**框架的动力学里只有一个长度**（r₀ = ħc/M₀），而世界的质量来自
  两个结构上不同的来源——**生成型** Λ_QCD（由无量纲 α_s 经维度transmutation 自动生出，就是我们的 M₀）
  vs **基本型** v（Higgs 势里的基本质量参数）。**待定长度的个数 = 独立长度的个数** ⟹ 必须 ≥ 2。
  标准模型在同一个位置上也没有闭合（v/Λ_QCD ≈ 250，即层级问题）。
  **对本层的直接后果**：μ 的主动产生**不能靠「把现有推导再做细」得到**——它是「缺第二个独立长度」，
  不是数值精度问题。**本层不做、也不假装能做这件事**（`fc5_locked` 一律 `unknown` 就是这条的机器化形式）。
- **DC 静磁学**：Forge 的场模型仍是真空圆环丝电流的精确静磁学，无 plasma、无 β 修正、无平衡。
  本层是把静磁学输出**喂进**上游的门，不是把 plasma 加进来。
- **不算排程/预算**：人·月、μ 阶梯（1e-4→1e-3→…）是排程假设与目标曲线，不进本层。
- **`fc5_locked` 一律 unknown**。
- 本层是**模型选择 + 上游已证条目的代入**，真但平凡；没有新物理预言。
