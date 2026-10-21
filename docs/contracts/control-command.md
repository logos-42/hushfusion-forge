# 契约：控制命令（Control Command Contract v0.1）

> 契约版本 `contracts.ContractVersion = "0.1"`。
> Go 实现：`internal/contracts/device.go`（`ControlCommand`）、`internal/contracts/safety.go`
> （`SafetyDecision`）；Schema：`internal/contracts/schema/control-command.schema.json`、
> `safety-decision.schema.json`。
> 判决实现：`internal/safety/`；闭环：`internal/control/`。

## 1. 命令是意图，判决才是许可

```text
AI 提议 ──► ControlCommand ──► Safety Kernel ──► SafetyDecision ──► 装置
              (意图)                (唯一入口)         (allow/deny)
```

`ControlCommand` 里**没有**"批准"字段。批准属于 `SafetyDecision`，且两者是**不同对象**：
把批准写进命令，等于允许命令自带通行证，安全内核就形同虚设。

## 2. 对象

```json
{
  "contract_version": "0.1",
  "command_id": "sim-001-cmd-3",
  "device_id": "sim-001",
  "kind": "set_actuator",
  "targets": [{"actuator": "coil", "value": 0.8}],
  "sequence": 8,
  "issued_at": "2026-10-21T00:05:00Z",
  "reason": "维持场强"
}
```

| 字段 | 规则 |
|---|---|
| `kind` | `set_actuator` / `safe_stop` / `shutdown` |
| `targets` | `set_actuator` 时必须非空；其它种类不携带 |
| `sequence` | 同一设备内**严格递增**（去重 + 乱序检测） |
| `issued_at` | 命令产生时刻（RFC3339）；新鲜度判据的输入 |

## 3. 安全判决

```json
{
  "contract_version": "0.1",
  "decision": "deny",
  "command_id": "sim-001-cmd-3",
  "device_id": "sim-001",
  "rules": ["R09"],
  "reason": "执行器 \"coil\" 目标 2.5 越界 [0, 2]",
  "evaluated_at": "2026-10-21T00:05:00Z"
}
```

`decision ∈ {allow, deny}`，**没有第三种**。`deny` 是**默认值**：任何"没检查到"
（版本错配、时间戳不可解析、未知执行器……）都落到 deny。

## 4. 判据表（`internal/safety`）

| 规则 | 判据 | 抓的失败模式 |
|---|---|---|
| R01 | 契约版本一致 | 交换格式错配的命令被当成合法命令 |
| R02 | 目标设备与当前状态设备一致 | 命令发错设备 |
| R03 | 命令种类在已知集合内 | 未知/拼错命令 |
| R04 | 设备模式允许动作类命令 | 非法状态转换 |
| R05 | 状态年龄 ≤ `MaxStateAge` | 依据过期状态行动 |
| R06 | 命令年龄 ≤ `MaxCommandAge` | 陈旧指令重放 |
| R07 | 命令序号严格大于状态序号 | 重复执行 / 乱序 |
| R08 | 执行器名在限幅表内 | AI 幻觉出不存在的执行器 |
| R09 | 目标值在 `[Min, Max]` 内 | 越限 |
| R10 | 单步 \|Δ\| ≤ `MaxRate` | 突变 |
| R11 | 停机/关机走逃逸路径（任何模式允许） | —— |

## 5. 三条设计取舍

1. **时间由调用方注入**（`Evaluate(cmd, state, policy, now)`）：判决可重放、可逐位复核，
   内核内部不取时钟。
2. **内核无状态**：判决是 (命令, 状态, 策略, 时刻) 的纯函数，没有随机、没有并发状态。
   状态机的**位置**在装置侧，安全内核只**检查**位置是否合法。
3. **停机命令不受限幅**：否则设备一旦越限就再也停不下来——越限后无法安全停机的系统，
   不是一个 fail-closed 的系统。

## 6. 与闭环的关系

`internal/control` 的闭环保证：

- 只有 `allow` 的命令才被作用到装置；
- `deny` 的命令**留在证据里**（被拒绝的尝试是审计对象，不是被丢弃的噪声）；
- AI 提议失败（掉线/超时/输出非法）时，闭环**自合成**一条 `safe_stop`，走 R11 逃逸路径，
  于是断网时装置进入 `safe_stop`，而不是保持上一条动作。

## 7. 诚实边界

- `internal/safety` **不认识任何物理量**。限幅与速率上限是**工程策略参数**（调用方给出），
  不是物理常数。它保证"命令不越过配置的边界"，不判断"这条命令在物理上是对的"。
- 本仓**没有硬件安全策略**：没有联锁、没有校准、没有驱动错误映射。阶段 7 之前禁止
  接入真实设备。
