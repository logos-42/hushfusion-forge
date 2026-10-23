// Package kernel 是 Bolloon Native Macro-Kernel 的 **Go 原生移植**。
//
// 背景: 该内核原是 TS/JS 包 (@bolloon/bolloon-kernel)，Forge 过去经一个 Node 子进程
// 桥 (bridge/server.mjs + stdio JSON-RPC) 调用它。本包把内核的「机制原语」用 Go 重写，
// 于是 Forge 不再需要 Node / 跨进程桥 —— 内核就是进程内的一个普通 Go 包。
//
// 移植保持与原内核**逐字一致的行为契约** (result 形状 / via 标记 / fail-closed 规则)，
// 这样原有调用方 (cmd/forge、os/tools/ossubmit) 与测试无需改语义即可切换到原生内核:
//
//   - channel.submit → ChannelActor 串行执行 (同 channel 内严格 FIFO, 跨 channel 隔离);
//   - lifecycle.submit / transport.submit / model.acquire → 端口注入, 未注入即拒 (fail-closed);
//   - bolloon.exec → 把一次 bolloon CLI 调用纳入 channel 调度 + exec 端口执行。
//
// 设计纪律 (延续原 kernel SDK 的出口纪律):
//
//	· 只做「机制」, 不判业务。
//	· 端口 (I/O) 语义由调用方给; 未注入 ⇒ fail-closed (ok:false 或报错), 不假装成功。
//	· 一律返回结果对象, 不抛 (生命周期/通信); 模型租约等构造期硬约束除外。
//
// 与 TS 版的已知差异 (诚实边界):
//   - 无 AsyncLocalStorage: 原内核用 ALS 做 mailbox 重入保护; Go 版提交是同步的、
//     每 actor 一把执行锁天然串行, 故不需要重入上下文 (deliverThroughActor 未随迁,
//     现有调用面不依赖它)。
//   - ModelRuntime 的并发等待队列 (maxConcurrency 槽 + 转让) 未随迁 (无调用面用到);
//     连接池 / 超时 / 取消 / 回退 / 熔断 / 退避已移植。
package kernel
