# @hibs/forge

HUSHFUSION Forge 的 npm 启动器与 Node.js SDK，版本 `0.2.5`。

首次运行会从 GitHub Release 下载对应 OS/arch 的 Go 二进制，缓存到：

```text
~/.hushfusion-forge/bin/0.2.5/
```

## CLI

```bash
npx @hibs/forge
# 或
npx @hibs/forge web --addr 127.0.0.1:8787 --open
# 全局安装后
forge
```

默认打开 Web/桌面外壳：`http://127.0.0.1:8787`。

使用已有二进制：

```bash
export HUSHFUSION_FORGE_BIN=/path/to/hushfusion-forge
npx @hibs/forge
```

## Node.js SDK

```js
const { ForgeClient, startServer } = require('@hibs/forge');

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
const { ForgeClient } = require('@hibs/forge');
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

npm 包版本与 GitHub Release / Go 模块标签均为 `v0.2.5`：

- https://github.com/logos-42/hushfusion-forge/releases/tag/v0.2.5
- `go install github.com/logos-42/hushfusion-forge/cmd/forge@v0.2.5`
