#!/usr/bin/env node
/**
 * Hushfusion Forge ↔ @bolloon/bolloon-kernel bridge
 *
 * 跨语言接线: Bolloon Native Macro-Kernel 是 TS/JS 包, Forge 是纯 Go。
 * 本 server 把 kernel 的**机制原语**暴露成 stdio JSON-RPC (每行一个 JSON),
 * Go 侧 (internal/kernelbridge) 通过子进程起它, 用 ndjson 对话。
 *
 * 协议 (JSON-RPC 2.0 over stdio, 一行一请求 / 一行一响应):
 *   请求  {"id":1,"method":"channel.submit","params":{...}}
 *   响应  {"id":1,"result":{...}}  或  {"id":1,"error":{"code":...,"message":"..."}}
 *
 * 已暴露的方法 (全部是 kernel 的「机制」, 不掺业务判定):
 *   channel.submit        — ChannelActor 串行执行 (同 channel 内严格 FIFO, ALS 重入)
 *   channel.peek          — 取 actor 状态快照
 *   lifecycle.submit      — Run 生命周期端口 (submitRunLifecycle, 未注入端口即拒)
 *   transport.submit      — 通信端口 (submitTransport, 未注入即拒)
 *   model.acquire         — ModelRuntime 只读租约 (acquire, 连接复用/超时/取消)
 *
 * 设计纪律 (来自 kernel SDK 的 index.ts):
 *   · 只暴露「机制」—— 不判"该不该继续"、不判"算不算完成"。
 *   · I/O 端口由调用方 (Go) 注入语义, 本 server 只做转译与记录。
 *   · 端口未注入 ⇒ fail-closed (返回 ok:false, 不假装成功)。
 */
import { createInterface } from 'node:readline';
import { spawnSync } from 'node:child_process';
import {
  ChannelActor,
  getOrCreateActor,
  ModelRuntime,
  submitRunLifecycle,
  submitTransport,
} from '@bolloon/bolloon-kernel';

/**
 * 命名端口注册表 —— JSON-RPC 传不了真实函数, 所以端口注入改用**名字**:
 * Go 侧写 `{"recordStep": "echo"}`, server 在这里解析成函数。
 * 这也是诚实边界的一部分: 端口**必须显式命名**, 未知名/未注入 ⇒ 内核 fail-closed。
 */
const NAMED_PORTS = {
  'recordStep.echo': async (_runId, _step) => ({ ok: true }),
  'send.echo': async (_peerId, _kind, _payload) => ({ ok: true }),
  'openConnection.fake': async () => ({
    id: 'fake-conn',
    call: async () => ({ ok: true }),
    close: async () => {},
  }),
  // exec.bolloon: 真正去调本机 bolloon 二进制（去中心化智能体 CLI）。
  // 二进制路径由 BOLLOON_BIN 环境变量覆盖（测试与非常规安装用）。
  // 这是 kernel 作为执行核心时，唯一接触外部进程的地方（端口注入，未注入即拒）。
  'exec.bolloon': async (args) => {
    const bin = process.env.BOLLOON_BIN || 'bolloon';
    const argv = Array.isArray(args) ? args.slice() : [String(args)];
    if (!argv.includes('--json')) argv.push('--json');
    const res = spawnSync(bin, argv, { encoding: 'utf8', maxBuffer: 1 << 20 });
    return {
      ok: res.status === 0,
      code: res.status,
      stdout: (res.stdout || '').trim(),
      stderr: (res.stderr || '').trim(),
    };
  },
};

/** 把 Go 侧给的 `{端口名: 命名端口}` 解析成函数形状; 未知名字 ⇒ 抛错 (调用方会看到 fail-closed) */
function resolvePorts(ports) {
  const out = {};
  for (const [key, value] of Object.entries(ports ?? {})) {
    const fn = NAMED_PORTS[`${key}.${value}`];
    if (typeof fn !== 'function') {
      throw new Error(`未知命名端口: ${key}.${String(value)} (可用: ${Object.keys(NAMED_PORTS).join(', ')})`);
    }
    out[key] = fn;
  }
  return out;
}

const rl = createInterface({ input: process.stdin, crlfDelay: Infinity });
const actors = new Map(); // channelId -> ChannelActor

// in-flight 请求数: line 事件 +1, 响应字节落地 (write callback) -1。
// stdin 关闭后仍要等所有在跑的 handler 把响应写完再退出 —— 否则 sleep 中的
// 异步任务会被 process.exit 直接杀掉, 响应静默丢失 (实测: 多请求时前几条无响应)。
let inflight = 0;
let stdinClosed = false;

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n', () => {
    inflight--;
    maybeExit();
  });
}

function maybeExit() {
  if (stdinClosed && inflight === 0) process.exit(0);
}

function fail(id, code, message) {
  send({ id, error: { code, message } });
}

/** ChannelActor 串行执行: fn 里 sleep 多久都行, 同 channel 不会并发 */
async function channelSubmit(params) {
  const channelId = params?.channelId;
  if (!channelId || typeof channelId !== 'string') throw new Error('params.channelId 必填');
  const actor = actors.get(channelId) ?? getOrCreateActor(`bridge:${channelId}`, {
    channelId,
    kind: params?.kind ?? 'bridge-task',
  });
  actors.set(channelId, actor);
  const taskId = params?.taskId ?? String(Math.random().toString(36).slice(2));
  const sleepMs = Number(params?.sleepMs ?? 0);
  const marker = String(params?.marker ?? taskId);
  const startedAt = Date.now();
  // 用真实 ChannelActor.submit 走 mailbox 队列 —— 这是被桥接的机制本体
  const result = await actor.submit(async (state) => {
    if (sleepMs > 0) await new Promise((r) => setTimeout(r, sleepMs));
    state.lastTask = { taskId, marker, at: Date.now() };
    return { taskId, marker, sleepMs, at: Date.now() };
  });
  return { ok: true, via: 'kernel-channel-actor', result, startedAt, finishedAt: Date.now() };
}

function channelPeek(params) {
  const channelId = params?.channelId;
  if (!channelId || typeof channelId !== 'string') throw new Error('params.channelId 必填');
  const actor = actors.get(channelId);
  if (!actor) return { ok: false, via: 'kernel-channel-actor', detail: 'actor 不存在' };
  return { ok: true, via: 'kernel-channel-actor', state: { ...actor.state } };
}

/** Run 生命周期端口: ports 语义由调用方给 (未注入 ⇒ 内核拒, fail-closed) */
async function lifecycleSubmit(params) {
  const req = params?.request;
  if (!req || typeof req !== 'object') throw new Error('params.request 必填 (RunLifecycleRequest)');
  const ports = resolvePorts(params?.ports ?? {});
  const out = await submitRunLifecycle(req, ports);
  return out;
}

/** 通信端口: 同样端口注入, 未注入即拒 */
async function transportSubmit(params) {
  const req = params?.request;
  if (!req || typeof req !== 'object') throw new Error('params.request 必填 (TransportRequest)');
  const ports = resolvePorts(params?.ports ?? {});
  const out = await submitTransport(req, ports);
  return out;
}

/** ModelRuntime 只读租约: openConnection 端口由调用方注入 */
async function modelAcquire(params) {
  const snapshot = params?.snapshot;
  if (!snapshot || typeof snapshot !== 'object') throw new Error('params.snapshot 必填 (ModelSnapshot)');
  const ports = resolvePorts(params?.ports ?? {});
  const runtime = new ModelRuntime(ports);
  const lease = await runtime.acquire(snapshot);
  return { ok: true, via: 'kernel-model-runtime', lease };
}

/**
 * bolloon.exec —— 把一次 bolloon CLI 调用放进内核 ChannelActor 里串行/审计执行。
 * kernel（@bolloon/bolloon-kernel）在这里是**执行核心**：它不判业务、只负责
 * 把这次外部进程调用纳入自己的 channel 调度与状态审计。真正的子进程由注入端口
 * exec.bolloon 完成。
 *
 * 纪律（对齐 bolloon kernel 的"端口注入 / 未注入即拒"）：
 *   - 本原语**不默认**任何端口 —— 调用方必须显式传 `ports: { exec: 'bolloon' }`，
 *     否则 resolvePorts 解析不出 exec 端口、actor.submit 内 `ports.exec` 未定义、
 *     内核直接拒（fail-closed）。这与 lifecycle/transport/model 一致。
 *   - 内核不替业务做"该不该 join 这个网络 / 该不该认领这个任务"的判断，只执行。
 *
 * 这是"核聚变"系统把 BOLLOON 当去中心化内核使用时，唯一经 kernel 编排的入口：
 * 外部多智能体交互（network join / task send / engine run …）都应经此走，
 * 而不是在 Go 侧裸调 bolloon。
 */
async function bolloonExec(params) {
  const args = params?.args;
  if (!Array.isArray(args)) throw new Error('params.args 必填 (string[])');
  const channelId = params?.channelId ?? 'bolloon';
  const actor = actors.get(channelId) ?? getOrCreateActor(`bridge:${channelId}`, {
    channelId,
    kind: 'bolloon-cli',
  });
  actors.set(channelId, actor);
  // 不默认端口：未注入 exec ⇒ 内核拒（fail-closed）。
  const ports = resolvePorts(params?.ports ?? {});
  const result = await actor.submit(async (state) => {
    const out = await ports.exec(args);
    state.lastBolloon = { args, at: Date.now() };
    return out;
  });
  return { ok: true, via: 'kernel-channel-actor', result };
}

const handlers = {
  'channel.submit': channelSubmit,
  'channel.peek': channelPeek,
  'lifecycle.submit': lifecycleSubmit,
  'transport.submit': transportSubmit,
  'model.acquire': modelAcquire,
  'bolloon.exec': bolloonExec,
};

rl.on('line', (line) => {
  inflight++;
  line = line.trim();
  if (!line) return;
  let req;
  try {
    req = JSON.parse(line);
  } catch (e) {
    send({ id: null, error: { code: -32700, message: `parse error: ${e.message}` } });
    return;
  }
  const { id, method, params } = req ?? {};
  const handler = handlers[method];
  if (!handler) {
    fail(id ?? null, -32601, `method not found: ${String(method)}`);
    return;
  }
  Promise.resolve()
    .then(() => handler(params ?? {}))
    .then((result) => send({ id: id ?? null, result }))
    .catch((err) => send({ id: id ?? null, error: { code: -32603, message: String(err?.message ?? err) } }));
});

rl.on('close', () => {
  stdinClosed = true;
  maybeExit();
});
