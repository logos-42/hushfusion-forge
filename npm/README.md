# hushfusion-forge

HUSHFUSION Forge 的 npm 启动器与 Node.js SDK，版本 `0.2.3`。

首次运行会从 GitHub Release 下载对应 OS/arch 的 Go 二进制，缓存到：

```text
~/.hushfusion-forge/bin/0.2.3/
```

## CLI

```bash
npx hushfusion-forge
# 或
npx hushfusion-forge web --addr 127.0.0.1:8787 --open
```

默认打开 Web/桌面外壳：`http://127.0.0.1:8787`。

使用已有二进制：

```bash
export HUSHFUSION_FORGE_BIN=/path/to/hushfusion-forge
npx hushfusion-forge
```

## Node.js SDK

```js
const { ForgeClient, startServer } = require('hushfusion-forge');

const server = await startServer({ addr: '127.0.0.1:8787' });
const client = server.client;

const boot = await client.bootstrap();
const session = await client.createSession(boot.state.active_agent_id);

for await (const chunk of client.chatStream(session.id, '你好')) {
  if (chunk.event === 'delta') process.stdout.write(chunk.data.text || '');
  if (chunk.event === 'done') console.log('\n' + chunk.data.reply);
}

server.stop();
```

已有服务时直接连接：

```js
const { ForgeClient } = require('hushfusion-forge');
const client = new ForgeClient({ baseUrl: 'http://127.0.0.1:8787' });
await client.bootstrap();
```

## 方法

- `bootstrap()` / `models()` / `settings(payload)`
- `createAgent(payload)` / `updateAgent(payload)`
- `createSession(agentId)` / `updateSession(payload)` / `deleteSession(id)`
- `branchSession(sessionId, messageIndex)` / `truncateSession(sessionId, fromIndex)`
- `shareSession(id)`
- `chat(sessionId, message, options)` / `chatStream(...)`
- `startServer(options)` / `ensureBinary(options)`

## 版本

npm 包版本与 GitHub Release / Go 模块标签均为 `v0.2.3`：

- https://github.com/logos-42/hushfusion-forge/releases/tag/v0.2.3
- `go install github.com/logos-42/hushfusion-forge/cmd/forge@v0.2.3`
