// 双核冗余心跳仲裁（对齐 bolloon src/kernel/ha/ha-arbiter.ts，纯函数可单测）。
//
// 纪律：所有仲裁逻辑都是纯函数（parse/isStale/decideTakeover），不碰 I/O；
// 写/读心跳在 guard 里经文件系统完成。心跳文件：
//   ~/.hushfusion/kernel-ha/<role>.heartbeat = JSON {pid,host,role,startedAt,lastBeat,runIds,kernelVersion}
import os from 'node:os';
import path from 'node:path';
import fs from 'node:fs';

export const BEAT_MS = 1000;
export const STALE_MS = 5000;

export function haDir(home = os.homedir()) {
  return path.join(home, '.hushfusion', 'kernel-ha');
}

export function heartbeatPath(role, home = os.homedir()) {
  return path.join(haDir(home), `${role}.heartbeat`);
}

export function makeHeartbeat(role, runIds = [], kernelVersion = '0.1.0') {
  const now = Date.now();
  return {
    pid: process.pid,
    host: os.hostname(),
    role,
    startedAt: now,
    lastBeat: now,
    runIds,
    kernelVersion,
  };
}

export function serializeHeartbeat(rec) {
  return JSON.stringify(rec);
}

export function parseHeartbeat(raw) {
  try {
    const rec = JSON.parse(raw);
    if (!rec || !rec.role) return { ok: false };
    return { ok: true, record: rec };
  } catch {
    return { ok: false };
  }
}

export function isHeartbeatStale(rec, now = Date.now(), staleMs = STALE_MS) {
  return now - rec.lastBeat > staleMs;
}

// pure 仲裁：secondary 是否应接管。
export function decideTakeover(primaryBeat, now = Date.now(), staleMs = STALE_MS) {
  if (!primaryBeat) return { takeover: true, reason: 'primary-heartbeat-missing' };
  if (isHeartbeatStale(primaryBeat, now, staleMs)) return { takeover: true, reason: 'primary-heartbeat-stale' };
  return { takeover: false, reason: '' };
}

export async function writeHeartbeat(rec, home = os.homedir()) {
  const target = heartbeatPath(rec.role, home);
  await fs.promises.mkdir(path.dirname(target), { recursive: true });
  const tmp = `${target}.tmp`;
  await fs.promises.writeFile(tmp, serializeHeartbeat(rec), { mode: 0o600 });
  await fs.promises.rename(tmp, target); // 原子改名，避免读到半截
}

export async function readPrimaryHeartbeat(home = os.homedir()) {
  try {
    const raw = await fs.promises.readFile(heartbeatPath('primary', home), 'utf-8');
    const { ok, record } = parseHeartbeat(raw);
    return ok ? record : null;
  } catch {
    return null; // 缺失 = primary 未起
  }
}
