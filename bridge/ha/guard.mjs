// 双核冗余看门循环（对齐 bolloon src/kernel/ha/ha-guard.ts）。
//
// 按节拍写自身心跳、读对端心跳；对端缺失/过期即触发 onTakeover。写/读经 arbiter
// 的文件系统函数完成；未启用则无动作（fail-closed，与 kernel 端口纪律一致）。
import {
  BEAT_MS,
  STALE_MS,
  makeHeartbeat,
  writeHeartbeat,
  readPrimaryHeartbeat,
  decideTakeover,
} from './arbiter.mjs';

export function makeGuard({
  role = 'primary',
  version = '0.1.0',
  beatMs = BEAT_MS,
  staleMs = STALE_MS,
  runIds = [],
  home,
  onTakeover,
} = {}) {
  let timer = null;
  let last = null;

  async function tick(now = Date.now()) {
    last = makeHeartbeat(role, runIds, version);
    last.lastBeat = now;
    await writeHeartbeat(last, home);
    // primary 不读对端；secondary 读 primary 心跳来仲裁。
    const peer = role === 'primary' ? null : await readPrimaryHeartbeat(home);
    const { takeover, reason } = decideTakeover(peer, now, staleMs);
    if (takeover && onTakeover) onTakeover(reason, last);
    return { takeover, reason, record: last };
  }

  function start() {
    if (timer) return;
    timer = setInterval(() => {
      tick().catch(() => {});
    }, beatMs);
    tick().catch(() => {}); // 立即写一次首跳
  }

  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
  }

  return { tick, start, stop, get last() { return last; } };
}
