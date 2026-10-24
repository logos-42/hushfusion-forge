'use strict';

const fs = require('node:fs');
const fsp = fs.promises;
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { Readable } = require('node:stream');
const { pipeline } = require('node:stream/promises');

const VERSION = '0.2.7';
const REPO = 'logos-42/hushfusion-forge';
const DEFAULT_BASE_URL = 'http://127.0.0.1:8787';

function platformName(platform = process.platform) {
  if (platform === 'win32') return 'windows';
  if (platform === 'darwin') return 'darwin';
  if (platform === 'linux') return 'linux';
  throw new Error(`不支持的平台: ${platform}`);
}

function archName(arch = process.arch) {
  if (arch === 'x64') return 'amd64';
  if (arch === 'arm64') return 'arm64';
  throw new Error(`不支持的架构: ${arch}`);
}

function binaryName(platform = process.platform) {
  return platform === 'win32' ? 'hushfusion-forge.exe' : 'hushfusion-forge';
}

function cacheRoot(version = VERSION) {
  return path.join(os.homedir(), '.hushfusion-forge', 'bin', version);
}

async function download(url, dest) {
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) {
    throw new Error(`下载失败 HTTP ${res.status}: ${url}`);
  }
  if (!res.body) throw new Error(`下载失败：响应为空 ${url}`);
  const tmp = `${dest}.download`;
  await fsp.mkdir(path.dirname(dest), { recursive: true });
  await pipeline(Readable.fromWeb(res.body), fs.createWriteStream(tmp));
  await fsp.chmod(tmp, 0o755);
  await fsp.rename(tmp, dest);
  return dest;
}

/**
 * 确保本机有对应版本的 Forge 二进制。
 * 首次运行会从 GitHub Release 下载当前 OS/arch 资产并缓存到 ~/.hushfusion-forge/bin/<version>/。
 */
async function ensureBinary(options = {}) {
  const explicit = process.env.HUSHFUSION_FORGE_BIN;
  if (explicit) {
    if (!fs.existsSync(explicit)) throw new Error(`HUSHFUSION_FORGE_BIN 不存在: ${explicit}`);
    return explicit;
  }

  const version = options.version || VERSION;
  const force = Boolean(options.force);
  const dest = path.join(cacheRoot(version), binaryName());
  if (!force && fs.existsSync(dest)) return dest;

  const platform = platformName();
  const arch = archName();
  const asset = `hushfusion-forge-${version}-${platform}-${arch}`;
  const url = `https://github.com/${REPO}/releases/download/v${version}/${asset}`;
  if (options.log !== false) {
    console.error(`hushfusion-forge: 下载 ${asset} ...`);
  }
  return download(url, dest);
}

function normalizeBaseUrl(baseUrl) {
  return String(baseUrl || DEFAULT_BASE_URL).replace(/\/+$/, '');
}

class ForgeClient {
  constructor(options = {}) {
    this.baseUrl = normalizeBaseUrl(options.baseUrl);
    this.headers = Object.assign({}, options.headers);
  }

  async request(pathname, options = {}) {
    const init = {
      method: options.method || 'GET',
      headers: Object.assign({}, this.headers, options.headers),
    };
    if (options.body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    const res = await fetch(this.baseUrl + pathname, init);
    const text = await res.text();
    let data;
    try {
      data = text ? JSON.parse(text) : null;
    } catch {
      data = text;
    }
    if (!res.ok) {
      const message = data && data.error ? data.error : (text || `HTTP ${res.status}`);
      throw new Error(message);
    }
    return data;
  }

  bootstrap() {
    return this.request('/api/bootstrap');
  }

  models() {
    return this.request('/api/models');
  }

  settings(payload) {
    return this.request('/api/settings', { method: 'POST', body: payload });
  }

  profile(payload) {
    return this.request('/api/profile', { method: 'POST', body: payload });
  }

  createAgent(payload) {
    return this.request('/api/agents', { method: 'POST', body: payload });
  }

  updateAgent(payload) {
    return this.request('/api/agents/update', { method: 'POST', body: payload });
  }

  createSession(agentId) {
    return this.request('/api/sessions', { method: 'POST', body: { agent_id: agentId } });
  }

  updateSession(payload) {
    return this.request('/api/sessions/update', { method: 'POST', body: payload });
  }

  deleteSession(id) {
    return this.request('/api/sessions/delete', { method: 'POST', body: { id } });
  }

  branchSession(sessionId, messageIndex) {
    return this.request('/api/sessions/branch', {
      method: 'POST',
      body: { session_id: sessionId, message_index: messageIndex },
    });
  }

  truncateSession(sessionId, fromIndex) {
    return this.request('/api/sessions/truncate', {
      method: 'POST',
      body: { id: sessionId, from_index: fromIndex },
    });
  }

  shareSession(id) {
    return this.request('/api/sessions/share', { method: 'POST', body: { id } });
  }

  chat(sessionId, message, options = {}) {
    return this.request('/api/chat', {
      method: 'POST',
      body: {
        session_id: sessionId,
        agent_id: options.agentId,
        message,
        images: options.images,
      },
    });
  }

  /**
   * 流式聊天：yield {event, data}，event 为 delta/done/error。
   */
  async *chatStream(sessionId, message, options = {}) {
    const res = await fetch(this.baseUrl + '/api/chat/stream', {
      method: 'POST',
      headers: Object.assign({}, this.headers, { 'Content-Type': 'application/json' }),
      body: JSON.stringify({
        session_id: sessionId,
        agent_id: options.agentId,
        message,
        images: options.images,
      }),
      signal: options.signal,
    });
    if (!res.ok) {
      const text = await res.text();
      throw new Error(text || `HTTP ${res.status}`);
    }
    if (!res.body) throw new Error('流式响应为空');

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buffer.indexOf('\n\n')) >= 0) {
        const raw = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        let event = 'message';
        let data = '';
        for (const line of raw.split('\n')) {
          if (line.startsWith('event:')) event = line.slice(6).trim();
          else if (line.startsWith('data:')) data += line.slice(5).trim();
        }
        if (!data) continue;
        let parsed;
        try {
          parsed = JSON.parse(data);
        } catch {
          parsed = { text: data };
        }
        yield { event, data: parsed };
      }
    }
  }
}

async function waitForServer(baseUrl, child, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    if (child && child.exitCode != null) {
      throw new Error(`Forge 进程提前退出，code=${child.exitCode}`);
    }
    try {
      const res = await fetch(baseUrl + '/api/bootstrap');
      if (res.ok) return;
    } catch (err) {
      lastError = err;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`等待 Forge 启动超时: ${lastError ? lastError.message : baseUrl}`);
}

/**
 * 后台启动 Forge Web 服务，并等待 /api/bootstrap 可用。
 */
async function startServer(options = {}) {
  const addr = options.addr || '127.0.0.1:8787';
  const baseUrl = normalizeBaseUrl(options.baseUrl || `http://${addr}`);
  const bin = await ensureBinary({ version: options.version || VERSION });
  const child = spawn(bin, ['web', '--addr', addr, '--open=false'], {
    cwd: options.cwd || process.cwd(),
    stdio: options.stdio || 'ignore',
  });
  await waitForServer(baseUrl, child, options.timeoutMs || 30000);
  return {
    child,
    url: baseUrl,
    client: new ForgeClient({ baseUrl }),
    stop() {
      if (child.exitCode == null) child.kill();
    },
  };
}

module.exports = {
  VERSION,
  REPO,
  DEFAULT_BASE_URL,
  ForgeClient,
  ensureBinary,
  startServer,
  platformName,
  archName,
  binaryName,
  cacheRoot,
};
