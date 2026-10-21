# 契约：设备（Device Contract v0.1）

> 契约版本 `contracts.ContractVersion = "0.1"`。
> Go 实现：`internal/contracts/device.go`（`DeviceState` / `DeviceMode` / `DeviceKind`）；
> Schema：`internal/contracts/schema/device-state.schema.json`。
> 仿真装置实现：`internal/plant/`。

## 1. 对象

```json
{
  "contract_version": "0.1",
  "device_id": "sim-001",
  "kind": "simulated",
  "mode": "running",
  "sequence": 7,
  "timestamp": "2026-10-21T00:05:00.123456789Z",
  "measurements": {"field": 1.25},
  "actuators": {"coil": 0.8},
  "fault_reason": ""
}
```

## 2. 字段规则

| 字段 | 规则 |
|---|---|
| `kind` | `simulated` / `hardware`。**本仓当前只有 `simulated`** |
| `mode` | 见 §3 状态机 |
| `sequence` | **单调递增**，每产生一个新状态 +1 |
| `timestamp` | RFC3339，状态产生的时刻（不是读到的时刻） |
| `measurements` | 传感器名 → 读数。**单位不在这里**：读数单位由传感器表定义 |
| `actuators` | 执行器名 → 当前值 |
| `fault_reason` | 非空表示处于故障/停机的原因 |

**为什么状态里必须有 `sequence` 和 `timestamp` 两个字段**：安全内核的新鲜度与去重
判据（R05/R06/R07）需要"这条状态有多新"与"它是第几代"两个可判定量。只有时间戳时，
同一时刻的两条状态无法区分先后；只有序号时，无法判断状态是否已经陈旧。

## 3. 模式状态机

```text
init → standby → running ⇄ standby
          ↓          ↓
       safe_stop ←──┘
          ↓
       shutdown
        fault ──(仅停机/关机可作用)
```

| 模式 | 含义 | 接受动作类命令？ | 接受停机/关机？ |
|---|---|---|---|
| `init` | 初始化中 | 否 | 是 |
| `standby` | 待命 | 是 | 是 |
| `running` | 运行中 | 是 | 是 |
| `fault` | 故障锁定 | 否 | **是**（逃逸路径） |
| `safe_stop` | 安全停机 | 否 | 是 |
| `shutdown` | 已关机 | 否 | 是 |

**`fault` 不是锁死**。故障态必须仍然可以被停掉，否则"fail-closed"会退化成"设备卡在
一个动不了的状态里"。停机/关机命令走安全内核的 **R11 逃逸路径**，不受模式与限幅约束。

## 4. 设备接口（为真实硬件预留）

未来任何硬件适配器必须满足下面这条链，且**AI 不直接接触驱动**：

```text
Sensor → DeviceState → （控制闭环）→ ControlCommand → Safety Kernel → Actuator
```

硬件适配层的额外要求（阶段 7，尚未实现）：

1. AI 不直接访问驱动；
2. 所有命令经过 Safety Kernel；
3. **本地联锁优先于远程命令**；
4. 断网自动进入安全状态；
5. 驱动错误不得伪装成正常数据；
6. 校准与固件版本进入证据；
7. 实验与控制命令可完整回放。

## 5. 诚实边界

- 本仓**没有任何硬件适配器**。`kind: "hardware"` 目前是契约里的一个取值，不是能力。
- `internal/plant` 的 `measurements` 是**占位模型**的读数，不是物理量，不得用于任何
  物理结论。
- 状态机只管"能不能动"，不管"该不该动"——该不该动是安全内核的判据（R01–R11）。
