#!/usr/bin/env node
'use strict';

const { ensureBinary, VERSION } = require('../lib/index.js');

function usage() {
  console.log(`HUSHFUSION Forge ${VERSION}

用法:
  forge                 启动 Web/桌面外壳（默认打开浏览器）
  forge web [args...]   启动 Web 外壳，其余参数透传
  forge --version       显示版本
  forge --help          显示帮助

环境变量:
  HUSHFUSION_FORGE_BIN             使用已有的 forge 二进制
  HUSHFUSION_FORGE_VERSION         指定要下载的版本（默认 ${VERSION}）
`);
}

async function main() {
  const argv = process.argv.slice(2);
  if (argv.includes('--version') || argv.includes('-v')) {
    console.log(VERSION);
    return;
  }
  if (argv.includes('--help') || argv.includes('-h')) {
    usage();
    return;
  }

  const version = process.env.HUSHFUSION_FORGE_VERSION || VERSION;
  const bin = await ensureBinary({ version });
  const args = argv.length > 0 ? argv : ['web', '--open'];

  const { spawn } = require('node:child_process');
  const child = spawn(bin, args, { stdio: 'inherit', cwd: process.cwd() });
  const forward = (signal) => () => {
    if (!child.killed) child.kill(signal);
  };
  process.on('SIGINT', forward('SIGINT'));
  process.on('SIGTERM', forward('SIGTERM'));
  child.on('error', (err) => {
    console.error(`forge: 启动失败: ${err.message}`);
    process.exitCode = 1;
  });
  child.on('exit', (code, signal) => {
    if (signal) process.exitCode = 1;
    else process.exitCode = code == null ? 1 : code;
  });
}

main().catch((err) => {
  console.error(`forge: ${err.message}`);
  process.exitCode = 1;
});
