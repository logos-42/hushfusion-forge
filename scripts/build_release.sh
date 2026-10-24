#!/usr/bin/env bash
# build_release.sh —— 打包 HUSHFUSION Forge 桌面/Web App 并发布到 GitHub Release。
#
# 产物（dist/）：
#   - Linux:   hushfusion-forge_<ver>_linux_<arch>.tar.gz
#   - Windows: hushfusion-forge_<ver>_windows_<arch>.zip
#   - macOS:   hushfusion-forge_<ver>_darwin_<arch>.tar.gz
#              Hushfusion Forge.app（darwin arm64/amd64 各一个 .app.zip）
#
# 用法：
#   VERSION=0.2.4 scripts/build_release.sh            # 打包并创建/上传 release
#   VERSION=0.2.4 scripts/build_release.sh --no-push  # 只打包，不动 GitHub
set -euo pipefail

VERSION="${VERSION:-0.2.4}"
PUSH=1
if [[ "${1:-}" == "--no-push" ]]; then PUSH=0; fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
DIST="$ROOT/dist"
rm -rf "$DIST"
mkdir -p "$DIST"

COMMIT="$(git rev-parse HEAD)"
LDFLAGS="-s -w -X main.buildCommit=$COMMIT"
BIN="hushfusion-forge"

cat > "$DIST/README.md" <<EOF
# HUSHFUSION Forge $VERSION

桌面/Web App：本地 HTTP + 内嵌前端。

## 运行
- macOS：解压 \`Hushfusion Forge.app.zip\`，把 \`Hushfusion Forge.app\` 拖进应用程序，双击运行。
- Linux：\`tar xzf hushfusion-forge_${VERSION}_linux_<arch>.tar.gz && ./hushfusion-forge web\`
- Windows：解压 zip，双击 \`hushfusion-forge.exe web\`

默认地址：http://127.0.0.1:8787
命令行：\`$BIN web --addr 127.0.0.1:8787 --open\`
EOF

cat > "$DIST/RELEASE_NOTES.md" <<EOF
## HUSHFUSION Forge v$VERSION

- 桌面/Web 外壳：侧边栏智能体/会话/账号，主对话窗口，身份化 DID。
- 点击「创建新智能体」自动建智能体 + 新会话。
- 侧边栏自动更新按钮：有更新即高亮，支持一键自动更新。
- 账号在侧边栏左下角，可设置头像与昵称。
- 平台能力：cron / node / python / git / hook / 子智能体集群 / Lean / env / workspace 分类加载。
- 去中心化频道：iroh / hyperswarm / orbitdb（Go↔Node 桥接）。

Commit: $COMMIT
EOF

build() {
  local os="$1" arch="$2"
  local out="$DIST/$BIN-$VERSION-$os-$arch"
  echo "build $os/$arch"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/forge
}

package_linux() {
  local arch="$1"
  local base="$BIN-$VERSION-linux-$arch"
  cp "$DIST/$base" "$DIST/$BIN"
  tar -C "$DIST" -czf "$DIST/$base.tar.gz" "$BIN" README.md RELEASE_NOTES.md
  rm -f "$DIST/$BIN"
}

package_windows() {
  local arch="$1"
  local base="$BIN-$VERSION-windows-$arch"
  cp "$DIST/$base" "$DIST/$BIN.exe"
  (cd "$DIST" && zip -qr "$base.zip" "$BIN.exe" README.md RELEASE_NOTES.md)
  rm -f "$DIST/$BIN.exe"
}

package_darwin_app() {
  local arch="$1"
  local base="$BIN-$VERSION-darwin-$arch"
  cp "$DIST/$base" "$DIST/$BIN"
  tar -C "$DIST" -czf "$DIST/$base.tar.gz" "$BIN" README.md RELEASE_NOTES.md

  local app="$DIST/Hushfusion Forge.app"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS"
  cp "$DIST/$base" "$app/Contents/MacOS/$BIN"
  cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Hushfusion Forge</string>
  <key>CFBundleDisplayName</key><string>Hushfusion Forge</string>
  <key>CFBundleIdentifier</key><string>ai.hushfusion.forge</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleExecutable</key><string>$BIN</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict>
</plist>
PLIST
  (cd "$DIST" && zip -qr "$BIN-$VERSION-darwin-$arch-app.zip" "Hushfusion Forge.app")
  rm -rf "$app" "$DIST/$BIN"
}

build darwin amd64;  package_darwin_app amd64
build darwin arm64;  package_darwin_app arm64
build linux amd64;   package_linux amd64
build linux arm64;   package_linux arm64
build windows amd64; package_windows amd64
build windows arm64; package_windows arm64

echo "== dist =="
ls -lh "$DIST"

if [[ "$PUSH" != "1" ]]; then
  echo "--no-push：未发布到 GitHub"
  exit 0
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "gh 未安装，跳过 GitHub 发布（产物在 dist/）"
  exit 0
fi

TAG="v$VERSION"
if ! gh release view "$TAG" >/dev/null 2>&1; then
  # 先建 release（不带资产）：大资产一次性创建容易触发 GitHub API EOF。
  gh release create "$TAG" --title "HUSHFUSION Forge $TAG" --notes-file "$DIST/RELEASE_NOTES.md" --verify-tag
fi
# 资产逐个上传 + 重试，避免一次 upload 全部导致连接中断。
for f in "$DIST"/*; do
  [ -f "$f" ] || continue
  ok=0
  for i in 1 2 3; do
    if gh release upload "$TAG" "$f" --clobber >/tmp/hushfusion-release-upload.log 2>&1; then
      ok=1
      break
    fi
    sleep 2
  done
  if [ "$ok" != "1" ]; then
    echo "资产上传失败: $f"
    tail -5 /tmp/hushfusion-release-upload.log
    exit 1
  fi
done
echo "published: https://github.com/logos-42/hushfusion-forge/releases/tag/$TAG"
