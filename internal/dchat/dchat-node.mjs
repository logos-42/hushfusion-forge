// dchat-node.mjs —— HUSHFUSION Forge 的去中心化通信 Node 助手。
//
// 支持的频道：
//   - hyperswarm：hyperswarm DHT + 连接（需 npm i hyperswarm）
//   - orbitdb：OrbitDB log + IPFS（需 npm i orbit-db ipfs-core）
//   - iroh：优先 @iroh/iroh；否则检测本机 `iroh` CLI（仅状态，fail-closed）
//
// 协议：stdin 每行一个 JSON 请求；stdout 每行一个 JSON 响应：
//   → {"cmd":"status|send|recv","channel":"...","topic":"...","body":"...","limit":20}
//   ← {"ok":true,"data":{...}} 或 {"ok":false,"error":"..."}
import readline from "node:readline";
import { spawnSync } from "node:child_process";

const channels = new Map(); // name -> { kind, ... }
const logs = new Map();     // `${name}:${topic}` -> [message]

function key(name, topic) {
  return `${name}:${topic}`;
}

function logMessage(name, topic, body, via) {
  const k = key(name, topic);
  if (!logs.has(k)) logs.set(k, []);
  const arr = logs.get(k);
  arr.push({ body, via, at: new Date().toISOString() });
  if (arr.length > 200) arr.shift();
}

async function getChannel(name) {
  if (channels.has(name)) return channels.get(name);
  if (name === "hyperswarm") {
    const mod = await import("hyperswarm");
    const sw = mod.default();
    const conns = [];
    sw.on("connection", (conn) => {
      conns.push(conn);
      conn.on("data", (d) => {
        const body = d.toString();
        logMessage(name, currentTopicFor(conn) || "default", body, "hyperswarm");
      });
      conn.on("error", () => {});
    });
    const obj = { kind: "hyperswarm", sw, conns };
    channels.set(name, obj);
    return obj;
  }
  if (name === "orbitdb") {
    // 兼容两种导出形态：orbit-db 有 default；ipfs-core 0.18 是纯 ESM 具名导出（create）。
    const odbMod = await import("orbit-db");
    const OrbitDB = odbMod.default || odbMod.OrbitDB || odbMod;
    const ipfsMod = await import("ipfs-core");
    const ipfsFactory = ipfsMod.default?.create || ipfsMod.create;
    if (typeof OrbitDB?.createInstance !== "function" || typeof ipfsFactory !== "function") {
      throw new Error("orbit-db/ipfs-core 模块加载异常（重装: npm install --prefix 助手目录）");
    }
    // 不监听不出网（本地 log 用途）：空 Swarm/Bootstrap 绕过老 libp2p 在新 Node
    // 上的 "no valid addresses" 启动失败；repo 目录带 nonce，失败重试不撞锁。
    const ipfs = await ipfsFactory({
      repo: "./forge-orbitdb-" + process.pid + "-" + Date.now(),
      offline: false,
      config: { Addresses: { Swarm: [], API: {}, Gateway: {} }, Bootstrap: [] },
    });
    const odb = await OrbitDB.createInstance(ipfs);
    const obj = { kind: "orbitdb", ipfs, odb, dbs: new Map() };
    channels.set(name, obj);
    return obj;
  }
  if (name === "iroh") {
    // 优先 JS 绑定；不可用则退到 CLI 检测（真实 send/recv 仍需 CLI/绑定实现）。
    try {
      const iroh = await import("@iroh/iroh");
      const node = await iroh.Iroh.getInstance();
      channels.set(name, { kind: "iroh-js", node });
      return channels.get(name);
    } catch {
      const r = spawnSync("iroh", ["--version"], { encoding: "utf8" });
      if (r.status === 0) {
        channels.set(name, { kind: "iroh-cli", version: (r.stdout || "").trim() });
        return channels.get(name);
      }
      throw new Error("iroh 不可用：安装 @iroh/iroh 或 iroh CLI");
    }
  }
  throw new Error(`未知频道 ${name}`);
}

function currentTopicFor(conn) {
  // hyperswarm 的 connection info 里带 topic；取不到就退回 default。
  try {
    const info = conn.info || {};
    const t = info.topics && info.topics[0];
    return t ? Buffer.from(t).toString("hex") : "default";
  } catch {
    return "default";
  }
}

async function handle(req) {
  const name = String(req.channel || "").toLowerCase();
  const topic = String(req.topic || "");
  if (!name) throw new Error("channel 为空");
  const ch = await getChannel(name);
  if (req.cmd === "status") {
    const out = { channel: name, kind: ch.kind, available: true };
    if (ch.kind === "iroh-cli") out.version = ch.version;
    if (ch.kind === "hyperswarm") out.peerId = ch.sw.peerId ? ch.sw.peerId.toString("hex") : null;
    if (ch.kind === "orbitdb") out.ipfs = true;
    return out;
  }
  if (!topic) throw new Error("topic 为空");
  if (req.cmd === "send") {
    const body = String(req.body || "");
    if (!body) throw new Error("body 为空");
    if (ch.kind === "hyperswarm") {
      ch.sw.join(Buffer.from(topic, "utf8"));
      if (ch.conns.length > 0) {
        for (const c of ch.conns) c.write(body);
        logMessage(name, topic, body, "hyperswarm");
        return { sent: true, peers: ch.conns.length };
      }
      logMessage(name, topic, body, "hyperswarm-queued");
      return { sent: true, peers: 0, queued: true };
    }
    if (ch.kind === "orbitdb") {
      const k = key(name, topic);
      let db = ch.dbs.get(k);
      if (!db) {
        db = await ch.odb.log(k, { accessController: { write: ["*"] } });
        ch.dbs.set(k, db);
      }
      await db.add(body);
      logMessage(name, topic, body, "orbitdb");
      return { sent: true, hash: (await ch.ipfs.id()).id };
    }
    throw new Error(`${name} 的 send 未实现（fail-closed）`);
  }
  if (req.cmd === "recv") {
    const limit = Math.max(1, Math.min(100, Number(req.limit) || 20));
    const k = key(name, topic);
    if (ch.kind === "orbitdb") {
      let db = ch.dbs.get(k);
      if (!db) {
        db = await ch.odb.log(k, { accessController: { write: ["*"] } });
        ch.dbs.set(k, db);
      }
      const out = [];
      for await (const e of db.iterator({ limit })) out.push(e.payload.value);
      return { channel: name, topic, messages: out.reverse() };
    }
    const arr = logs.get(k) || [];
    return { channel: name, topic, messages: arr.slice(-limit).map((m) => m.body) };
  }
  throw new Error(`未知命令 ${req.cmd}`);
}

const rl = readline.createInterface({ input: process.stdin, terminal: false });
for await (const line of rl) {
  const s = line.trim();
  if (!s) continue;
  let req;
  try {
    req = JSON.parse(s);
  } catch (e) {
    process.stdout.write(JSON.stringify({ ok: false, error: "请求不是 JSON" }) + "\n");
    continue;
  }
  try {
    const data = await handle(req);
    process.stdout.write(JSON.stringify({ ok: true, data }) + "\n");
  } catch (e) {
    process.stdout.write(JSON.stringify({ ok: false, error: String(e && e.message ? e.message : e) }) + "\n");
  }
}
