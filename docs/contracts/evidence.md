# 契约：证据（Evidence Contract v0.1）

> 契约版本 `contracts.ContractVersion = "0.1"`。
> Go 实现：`internal/contracts/api.go`（`EvidenceRef` / `EvidenceKind`）；
> 轨迹实现：`internal/control/loop.go`（`Trace` / `StepRecord`）。

## 1. 证据是引用，不是内容

```json
{
  "kind": "trace",
  "uri": "trace:sim-001-2026-10-21T00:00:00Z",
  "sha256": "3f2a…",
  "forge_version": "0.2",
  "produced_at": "2026-10-21T00:05:03Z"
}
```

| 字段 | 规则 |
|---|---|
| `kind` | `trace` / `record` / `gate` / `report` / `external` |
| `uri` | 证据位置（仓内相对路径 / URL / 内容地址） |
| `sha256` | 内容指纹。**为空时该证据不得用于判决** |
| `forge_version` | 产出它时的**判据版本**（`config.ForgeVersion`），不是契约版本 |
| `produced_at` | 产出时刻 |

**为什么"引用必须带指纹"**：一份会变的证据，引用它的结论就会跟着变。没有指纹的引用
在审计时等于没有引用——它只说" somewhere 有个东西"，不能证明结论是从**那一份**内容
推出来的。

## 2. 控制轨迹（Trace）

一次闭环运行产生一条 `Trace`：

```json
{
  "contract_version": "0.1",
  "trace_id": "trace-1",
  "device_id": "sim-001",
  "started_at": "2026-10-21T00:00:00Z",
  "steps": [
    {
      "step": 1,
      "state_before": { … },
      "command": { … },
      "decision": { … },
      "applied": true,
      "state_after": { … }
    }
  ]
}
```

每一步记录**完整**的四件套：

| 字段 | 为什么必须有 |
|---|---|
| `state_before` | 依据什么状态做的判断 |
| `command` | 送进安全内核的**原始**命令（未被改写） |
| `decision` | 安全内核的判决 + 命中规则 + 理由 |
| `applied` | 是否真的作用到了装置 |
| `apply_error` | 作用失败的原因（安全通过但装置拒绝，例如非法状态转换） |
| `state_after` | 作用后的状态 |
| `proposal_error` | AI 提议失败的原因（掉线/超时/非法输出） |

**被拒绝的尝试也在轨迹里**。只记成功步骤的轨迹是"幸存者偏差日志"：它看不出系统被
拒绝过多少次，也看不出拒绝的原因分布。

## 3. 可重放性

`Trace.SHA256()` 是对轨迹紧凑 JSON 的 SHA-256。给定同样的输入序列（同一状态机、同一
提议序列、同一时钟），轨迹指纹必须**逐位相同**——这条由单测钉住
（`internal/control`：`TestTraceIsReproducibleAndCarriesEvidence`）。

前提：闭环的时钟是**注入的**，装置是**确定性的**。任何引入真实时钟或随机源的环节都会
破坏这条性质，届时必须把该随机源显式记录进轨迹，而不是让它藏在过程里。

## 4. 与既有证据体系的关系

本仓已有的证据体系**继续有效**，本契约是它的补充而不是替换：

| 既有 | 位置 | 关系 |
|---|---|---|
| 注册表记录 | `runs/<tag>/registry.jsonl` | `kind: "record"` |
| 门禁结果 | `scripts/verify.sh` | `kind: "report"` |
| 黄金 trace | `testdata/world_trace_golden*.jsonl` | 世界协议层的 trace，与控制轨迹是不同层 |
| 锚点 | `testdata/projectionphysics_anchors.json` | `kind: "external"`（来自上游） |

`forge_version` 字段把"这份证据是用哪一版判据产出的"写进证据本身，这是
`docs/version-0.1.2.md` §3 版本模型在控制平面的延续：**用今天的判据复现昨天的 run**
必须可被机器判定。

## 5. 诚实边界

- 轨迹记录**发生了什么**，不证明**为什么该发生**。物理正确性由设计判决层负责。
- `sha256` 校验内容**未被改动**，不校验内容**是对的**。
- 本轮尚未把轨迹落盘到 `runs/` 或独立证据目录；目前只在内存中生成并可指纹化。
