# Forge World Protocol（冻结契约）

> **当前版本 v2** ｜ v1 仍受支持(`--protocol 1`),**v1 的黄金 trace 与门 G18 继续有效**。
> 正文 §1–§7 是 v1 的文本;v2 的全部增补集中在 **§8**,逐条写明它覆盖了正文的哪一段。
> **§8 与正文冲突时,以 §8 为准**(这是版本变更的记录方式,不是"两处说法"——审计时只认这一条明线)。
> v2 的**判据与计量协议**在 `docs/world-structure.md`(那份冻的是"够不够格检验 Options")。

> 本文件冻结 **Go 世界（`forge world serve`）与 Python Agent（headless）之间的线协议**。
> 契约先行：先冻这份文本，再写实现；实现与文本不一致时，**文本是权威**，改动即协议版本变更。
>
> 一句话:本协议**不新增物理、不新增时序结构、不含学习**;它只负责把 `internal/rlenv` 的语义
> 原样、逐位、可复现地搬到进程边界之外,让另一个语言里的 agent 能诚实地使用这个设计世界。

---

## 0. 为什么需要它

1. **语言与仓的边界是硬的**:引擎在 Go(`internal/`),agent 在 Python(headless)。本仓纪律禁止
   Python `import` Go 的任何东西,也禁止"用 Go 二进制去验证 Go"。剩下唯一诚实的形状是**进程间协议**。
2. **没有协议,跨语言实验的差异无法归因**:同一条轨迹在两边的数不一样时,分不清是物理、是序列化、
   还是客户端接线。协议把"世界的语义"变成可以**逐字节比对**的东西。
3. **上游需求**:headless(OaK / 持续学习)需要一个可选的世界来跑预测层与(将来的)时间抽象。
   它自己的最新结论是「在**没有时序结构**的环境上测 Options 是混杂的」——本版本**故意不提供**时序结构,
   因此它**不是**那个实验的场地;P1 的判据见 §7。

---

## 1. 传输与进程

```
forge world serve [--protocol 2] [--max-steps n] [--delta-scale s] [--record <file>] [--replay <file>]
                  [--source MATBG_N2_perp|MATBG_N2_par] [--budget n] [--target s]   # v2 新增
```

- **JSONL 行协议**:一行一个请求,一行一个响应,按到达顺序一一对应。不允许跨行、不允许批量、不允许并发复用。
- **stdout 只放协议行**;人类日志、warning、进度一律走 **stderr**。往 stdout 写任何非协议内容 = 协议违规
  (客户端会因为一行噪声而读到错位的响应 —— 这正是"静默损坏"的形状)。
- **退出码**:`0` 正常(收到 `close`,或 stdin EOF);`1` 致命错误(已回 `ok:false` 之后);`2` 用法错误。
- 一行的大小不做限制,但**必须**是单个 JSON 对象,不以逗号/换行结尾之外的任何修饰。

---

## 2. 数值与逐位复现(本协议最容易被写坏的地方)

- 所有浮点是 IEEE754 `float64`。序列化**必须最短往返**:Go `strconv.FormatFloat(v, 'g', -1, 64)`,
  Python `repr(float)`。判据是 `parse(serialize(v)) == v` **逐位**成立。
- **禁止**任何"看起来一样"的写法:`%.6g`、四舍五入、定点字符串、先转 float32 再打印 —— 一律违规。
- 世界是**确定性**的:同一段请求序列必须产出同一段响应序列,**逐字节相同**。世界内部**不使用**
  未播种的随机源;需要随机时由 `reset` 显式给 `seed`(见 §3.2)。
- 逐位复现的对象是**响应字节**,不是"数值接近"。任何"差 1e-15 也算过"的判据都不属于本协议的门。

---

## 3. 消息

### 3.1 `hello`(握手,客户端必须先做)

请求：`{"op":"hello"}`

响应：

```json
{"ok":true,"protocol":1,
 "action_dim":9,"observation_dim":16,
 "obs_metric_keys":["B_mid_T","B_throat_T","mirror_ratio","volume_good","ripple","B_coil_max_T","cost_proxy"],
 "obs_metric_refs":[1.0,1.0,1.0,1.0,0.1,12.0,1.0],
 "max_steps":20,"delta_scale":0.15,
 "spec":{"n_coils":3,"n_params":9,"lower":[...],"upper":[...],"sha256":"..."},
 "engine":{"version":"0.1.x"}}
```

- 客户端**必须在 hello 之后自己核对** `action_dim` / `observation_dim` / `obs_metric_keys` /
  `obs_metric_refs` / `spec.sha256`。不一致时**立刻非零退出并报错**,不许"先跑再看"。
  这条是 §6 第 3 条门的对象:握手错了却在跑,是比崩溃更坏的失败。
- `spec.sha256` 是 spec 的规范 JSON（键排序、最短往返浮点）的 sha256。

### 3.2 `reset`

请求：`{"op":"reset","x0":[...]}` 或 `{"op":"reset","seed":12345}`

响应：`{"ok":true,"obs":[...],"info":{...}}`

- `x0` 显式给出时**必须**是 `action_dim` 长度;先夹进 spec 盒子再求值(与 `rlenv.Env.Reset` 一致)。
- 用 `seed` 时,世界用**显式播种**的生成器在盒子内均匀取点;**不允许**取进程全局随机源。
- 重复 `reset` 是合法的:它开始一条新的 episode。

### 3.3 `step`

请求：`{"op":"step","action":[...]}`

响应：`{"ok":true,"obs":[...],"reward":r,"terminated":false,"truncated":b,"info":{...}}`

- 语义 = `rlenv.Env.Step`:动作是**归一化增量**(裁剪到 `[-1,1]`),`dx[i] = a[i]*DeltaScale*(upper[i]-lower[i])`,
  结果再夹进盒子;`reward = score(之后) − score(之前)`。
- `terminated` 在本世界**恒为 `false`**(设计空间没有吸收态);`truncated = (step >= max_steps)`。
- **未 `reset` 就 `step`** 是错误(`not_started`),不是空操作。
- **已结束之后再 `step`** 不是错误:返回 `terminated=true, truncated=true, reward=0`,且**不**再消耗求值
  (与 `Env.Step` 在 done 之后的行为一致)。客户端不许把它当成"又走了一步"。

### 3.4 `close`

请求：`{"op":"close"}` → 响应 `{"ok":true}`,随后进程以 `0` 退出。stdin EOF 等价于 `close`。

### 3.5 错误

响应：`{"ok":false,"error":{"code":"<冻结码>","message":"..."}}`

冻结的错误码：

| code | 触发 |
|:--|:--|
| `bad_json` | 行不是合法 JSON 对象 |
| `unknown_op` | `op` 不在 {hello,reset,step,close} |
| `bad_field` | 缺字段 / 类型错 / `x0` 与 `seed` 同时缺 |
| `dim_mismatch` | 向量长度 != `action_dim` |
| `not_started` | 未 reset 就 step |
| `unsupported_protocol` | `--protocol` 不是已知版本;或 hello 之后客户端声明了不支持的版本 |
| `internal` | 世界内部 panic:转成错误之后**必须退出非零** |

**规则**:宁可大声失败,**绝不编造数字**。环境无法给出诚实答案时不许悄悄回 0.0 / 空观测 / 默认值 ——
这与 `rlenv.LoadPolicy` 返回 `ErrNoLearnedPolicy` 是同一条策略,也是本仓 G7 反造假门的精神。

---

## 4. `info` 字段(冻结)

```json
{"score":0.0,"delta_score":0.0,"feasible":false,"design_id":"D0001","step":0}
```

- 键名与 `rlenv.Info` 的 JSON tag **逐字相同**;不得增删、不得改名、不得把 `delta_score` 与 `score` 混用。
- `step` 是 `reset` 以来访问过的设计数(0 = reset 时的设计)。
- `design_id` 由注册表分配。**trace 模式**(§6)下注册表落在 `runs/scratch/`(已被 `.gitignore` 忽略),
  且每个 trace 进程从空注册表开始 —— 因此同一条 trace 的 `design_id` 序列是确定性的、可复现的。
  **被提交的证据是 trace 文件本身,不是那次跑的 `runs/` 目录。**

---

## 5. 语义的唯一权威

- 本协议**不定义物理、不重复物理**:语义 = `internal/rlenv/api.go`(观测布局、动作缩放、奖励定义、
  终止条件、裁剪顺序)。协议层只做搬运与校验,**不得**在此之上加任何 shaping、任何额外惩罚、任何"顺手"
  的归一化。
- 观测布局(抄自 `rlenv`,供客户端实现时对账,数值上以代码为准):
  `obs = [2*(x[i]-lower[i])/(upper[i]-lower[i])-1 | metric(key)/ref]`,`observation_dim = n_params + 7`。
- **本版本明确不提供**:时序结构 / Options / reward shaping / 学习 / 跨版本兼容。
  任何"顺手把历史塞进观测"的改动都不属于 v1。

---

## 6. 门(G18)

被提交的证据:`testdata/world_trace_golden.jsonl`(一次确定性 session 的请求与响应)。

**trace 文件格式(冻结)**:每行一个对象,`req` 与 `resp` 的值是**当时的原始字节**:

```text
{"req":<原始请求行>,"resp":<原始响应行>}
```

实现方式必须是**字符串拼接**,不许把请求/响应重新序列化一遍再写进 trace —— 重新序列化正是丢字节的地方。
Python 侧同理(必须拼接,不许 `json.dumps`)。

G18 是**三条一起**判:

1. **Go 回放**:`forge world serve --replay testdata/world_trace_golden.jsonl` —— 把 trace 里的每条 `req`
   喂给一个新建的世界,产出的响应必须与 `resp` **逐字节**相同。任一行不同即红,并打印该行的行号与差异。
2. **Python 回放**:`python3 python/aux/world_client.py --replay testdata/world_trace_golden.jsonl` ——
   同样逐字节比对。**这条才是跨语言证据**:它证明 Python 客户端看到的世界与 Go 写下的是同一个世界。
3. **握手自校验(反向断言)**:把一个**故意改坏**的 hello 喂给客户端(维度或 spec 哈希不符),客户端
   **必须非零退出**。只测好消息的门不算门。

生成方式:`forge world serve --record testdata/world_trace_golden.jsonl`,由 trace 内的
`hello → reset → step×n → close` 构成(`n` 取 5)。

**这份 trace 覆盖什么、不覆盖什么(写准,别假装)**:

- 覆盖: 握手 → 显式 seed 的 reset → 归一化动作 → **双重裁剪**(动作裁剪 + 盒子夹取;
  实测 x0 与 obs 都撞到 ±1) → 注册表分配的 `design_id` 序列 → 关闭。
- **不覆盖 `truncated=true`**: `hello.max_steps` 是默认的 20,而 trace 只走 5 步,
  所以 `truncated` 在整份 trace 里恒为 `false`。要覆盖它需要 `--max-steps 5` 另录一份,
  那会让 `hello.max_steps` 与 §3.1 的示例不一致 —— 本版本选择**不假装覆盖**:
  `truncated=true` 与「done 之后再 step」的语义由 `internal/world/world_test.go` 的单测
  盯住,而不是由这份跨语言 trace 盯住。
- 尺度锚点(漂移可见):trace 8 行;响应的原始字节合计 3635;B 级证据。

---

## 7. 诚实边界(写在这里,不许在别处美化)

1. **这还不是那个实验的场地。** 本协议暴露的是 `rlenv`——一个**无时序结构**的设计评分环境
   (`A ≡ 0`:同一状态在不同历史下的最优动作相同)。headless 的 Options 在自己的 `lm4` 上
   正是因为这个原因得到过一个**混杂的负结果**。所以 P1 **必须先证明时序结构存在**
   (`A > 0`,照 headless 的检验法:同一状态不同历史下最优动作不同,且四档特征量能互相区分),
   **通过之前不许把 Options 接上来**。
2. **协议不吃掉 P1 的工作。** "把门变成时序结构"是 `World` 层的语义改动,不是协议改动;
   若 P1 需要新的消息(历史的读取、预算的读取),那是 **protocol v2**,要走同样的冻结流程。
3. **不做的事**:不把 `registry.jsonl` 当经验喂给 agent(那是抄答案,不是持续学习);
   不在本仓实现 RL 算法(黑名单:堆算法比 reward);不用被动观测声称完成了控制。
4. **证据等级**:本协议与 G18 属于 B 级(已实现、可复现),**不给收益**。
   任何"接上 headless 之后更好"的说法都必须走 C 级纪律:多 seed、共享对照、报告 Q7
   (第二次进入同一 regime 的 adaptation 步数是否更短),而不是单次 reward。

---

## 8. v2 增补（冻结,与正文冲突时以本节为准）

**动因**:`docs/world-structure.md` 的 P1 判据。v1 暴露的 `Env` 是**可交换**的(`A ≡ 0`),
在任何这类世界上检验时间抽象都是混杂的。v2 让世界有**可被抽象的顺序依赖**,并**先证明它有**(G19)。
**v2 不新增物理**:`internal/design` / `internal/physics` 一行不动。

### 8.1 覆盖 §1(用法与回放)

- 交互服务默认 `--protocol 2`;`--protocol 1` 保留 v1 语义(供 G18 与旧 trace)。
- **`--replay` 忽略 `--protocol`,以 trace 自己的 `hello` 响应里的 `protocol` 字段为准**
  (trace 是那段历史的权威)。理由:否则同一份黄金 trace 会因为默认版本前进而假红,
  而"默认值"和"协议兼容性"是两件事。
- 新旗标:`--source`(初始场源)、`--budget`(步数预算)、`--target`(终止目标分)。
  三者也都可以由 `reset` 的 `regime` 字段逐个覆盖(见 8.3)。**默认值只住 `internal/config`。**

### 8.2 覆盖 §3.1(`hello`)

响应增加 v2 字段(键名冻结):

```json
{"ok":true,"protocol":2,
 "action_dim":12,"observation_dim":26,
 "obs_keys_v2":["...19 维同 v1...","budget_remaining_norm","depth_norm",
                "resid_death","resid_mu_window","resid_coil_ceiling",
                "source_onehot_perp","source_onehot_par"],
 "budget":24,"target":1.0,"delta_scale":0.15,"max_steps":24,
 "sources":["MATBG_N2_perp","MATBG_N2_par"],
 "clamp_zones":{"coil_proximity_floor_m":0.005},
 "spec":{...,"sha256":"..."},"engine":{"version":"0.1.x"}}
```

- v1 的 `obs_metric_keys` / `obs_metric_refs` / `max_steps` / `spec` **保留不动**(v1 测试仍过)。
- `observation_dim` 在协议 2 下是 **26**;客户端必须按 `protocol` 选维度表,不许"先跑再看"。

### 8.3 覆盖 §3.2(`reset`)

`reset` 增加可选的 `regime` 对象,缺省取服务旗标:

```json
{"op":"reset","seed":12345,
 "regime":{"source":"MATBG_N2_par","budget":8,"target":1.0,"x0":[...]}}
```

四档 regime 的定义(`{perp, par} × {tight 8, loose 24}`)与"必须互相可区分"的判据
在 `docs/world-structure.md` §3。`x0` 与 `regime` 同时给出时 `x0` 优先(先夹进盒子再求值)。

### 8.4 覆盖 §3.3(`step`)

- **动作语义**:v2 与 v1 同(归一化增量 + 夹取),但**增量加在"当前"参数上**,而夹取里包含
  物理层自身的 θ-依赖钳位(近导线 5 mm 的 `alpha2` 钳位 = 官方定义、线圈峰场天花板、μ 窗口边界)。
  ⇒ 这是 `A ≠ 0` 的真来源之一(R2),**不是**我们新造的规则。
- **前提门(R3)**:当前设计处在"夹取区"(线圈距打分点 < 5 mm 或 `B_coil_max_T` 超材料天花板)时,
  **只接受把设计移出该区的动作**;其余动作 `info.rejected=true`,**仍消耗一步预算**。
- **奖励(R1)**:`reward = Δscore − λ_cost · max(0, Δcost_proxy) / cost(θ_prev)`;
  `λ_cost` 住 `internal/config`。`cost_ref` **不用常数**:用该 episode 初始设计的真 cost(由 `reset` 算出)。
- **代价记账**:每次 `step` 记 1 步预算,`info.budget_remaining` 是**已扣完的**剩余值。
- **覆盖 §3.3 的两条"恒为 false"**:`truncated = (已用步数 ≥ budget)`;
  **`terminated` 在 v2 可以真** = `(真分数 ≥ regime.target)`。这是**游戏规则声明,不是物理**,
  写在这里是为了不许它被当成物理(黑名单:R5 不是 A 的来源)。
- **`info` 增补**:`rejected`(bool)、`budget_remaining`(int)、`depth`(int)、
  `residuals`(3 个真约束余量)、`source`(字符串)、`clamped`(哪些参数撞到了盒子/钳位)。

### 8.5 覆盖 §3.5(新消息 `set_source`)

请求：`{"op":"set_source","source":"MATBG_N2_perp"}`　响应：`{"ok":true,"obs":[...],"info":{...}}`

- 合法值只有 hello 的 `sources`;其他值 = `bad_field`。
- 语义:**用新源对同一个 θ 重新求值**(两个源都是上游真源:0.12 T / 1.6 T),
  死活判据与 μ 窗口余量随源重算 ⇒ R3 的开关状态可能翻转。**消耗 1 步预算**;预算已尽 = `truncated`。
- 未 `reset` 就 `set_source` 是错误(`not_started`)。

### 8.6 对 §6 的增补:门 G19

G19 的三条子门与判据全部在 `docs/world-structure.md` §5(尺子自检 / 结构门 / 四档可区分),
证据存档 `testdata/world_structure_v2.json`。**G19 红时不许启动 P2/P3** —— 接 Options 的前提是
`A` 到了强档,这是 `docs/world-structure.md` §4 预注册的那张表说了算,不是"看起来差不多"说了算。

### 8.7 对 §7 诚实边界的增补

v1 的 §7 写着"本版本故意不提供时序结构,因此它不是那个实验的场地"。v2 把这句话推进一格:

- v2 **提供**了顺序依赖,并且**要求 G19 先证明它真的存在**(R1 不算结构,R5 不算结构);
- 即便 G19 到了强档,也只说明**这个世界可以用来检验 Options**,
  **不说明 Options 有用** —— 那要 P3 的 K1 四档矩阵 + 消融(共享预训练 + 随机基线 + ≥2 路对照)。
- G19 若停在零档/弱档,诚实结论是「**本世界规则下设计空间仍不可交换性不足,不许接 Options**」,
  处置写在 `docs/world-structure.md` §4(转 P4 或重设计世界语义),**不许**调阈值或换动作集合把它变成绿的。
