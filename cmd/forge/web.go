package main

// web.go —— 桌面/Web 外壳入口：本地 HTTP + 内嵌前端（见 internal/webapp）。
// 这是“打包桌面 App”的本体：各平台产物都是同一个二进制，启动后打开本地 Web UI。

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/envload"
	"github.com/logos-42/hushfusion-forge/internal/webapp"
)

func cmdWeb(args []string) int {
	fs := newFlagSet("web", "forge web [--addr HOST:PORT] [--open] [--dir DIR]",
		"启动 HUSHFUSION Forge 桌面/Web 外壳（本地 HTTP + 内嵌前端）。")
	addr := fs.String("addr", "127.0.0.1:8787", "监听地址（本机默认 127.0.0.1）")
	open := fs.Bool("open", true, "启动后自动打开浏览器")
	dir := fs.String("dir", "", "状态目录（缺省 ~/.forge/web）")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	// 自更新：新进程等旧进程释放端口后再监听。只由 restart.go 注入。
	if v := os.Getenv("HUSHFUSION_RESTART_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			time.Sleep(d)
		}
	}
	root := repoRoot()
	base := os.Getenv("HUSHFUSION_OS_DIR")
	if base == "" {
		base = filepath.Join(root, "os")
	}
	// Web/App/CLI/TUI/SDK 共用同一份 workspace 模型配置与 .env。
	applyWorkspaceConfig(base)
	if _, err := envload.Load(root); err != nil {
		note("web: .env 加载失败: %v", err)
	}
	srv, err := webapp.NewWithRoot(*addr, *dir, root)
	if err != nil {
		return fail("web: %v", err)
	}
	url := "http://" + *addr
	if *addr == "" {
		url = "http://127.0.0.1:8787"
	}
	note("HUSHFUSION Forge Web 外壳: %s", url)
	note("  版本 %s · 状态目录 %s", config.ForgeVersion, *dir)
	if *open {
		_ = openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil {
			return fail("web: %v", err)
		}
	case <-ctx.Done():
		note("正在停止 Web 外壳…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	return 0
}

// openBrowser 尽力打开系统浏览器（桌面 App 体验）；失败不影响服务。
func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}
