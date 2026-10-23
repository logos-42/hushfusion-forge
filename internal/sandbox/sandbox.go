// Package sandbox 给外部进程执行加一道围栏（fence）。
//
// 它取代"裸 os/exec spawn"的位置，提供四件可移植、可测的硬约束：
//
//   - 目录 jail：命令的 cwd 被钉在 Policy.Root；JailPath 拒绝任何逃逸 root 的路径
//     （.. 穿越、绝对路径越界）——fail-closed。
//   - env 白名单：只有 EnvAllow 点名的宿主变量才透传给子进程，其余一律丢弃；
//     EnvSet 可再注入/覆盖。默认连 PATH 都不给（要显式列）。
//   - 进程组隔离：子进程独立 pgid；超时/取消时 kill 整个组（-pgid），
//     不会留下"杀父留子"的孤儿。
//   - 超时：Policy.Timeout 到点先 TERM 给 KillGrace，再 KILL。
//
// 诚实边界（未实现，不假装）：
//   - 资源上限（RLIMIT_CPU/AS/NOFILE/FSIZE/NPROC）未强制。要在 Linux/darwin 上
//     真强制需要"自重exec 子进程 + syscall.Setrlimit"或平台专属 SysProcAttr，
//     且无法在本机（darwin）交叉验证 Linux 侧字段——列为下一步，做时会带真跑门。
//   - 不提供网络隔离（无 netns/iptables）；要断网需在外层（容器/防火墙）保证。
//   - 符号链接逃逸：JailPath 用 EvalSymlinks 做尽力检查，竞态下不保证。
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Policy 是一条执行围栏。
type Policy struct {
	// Root 是 jail 根（子进程 cwd）；为空表示不切换 cwd（仍可用 JailPath 校验路径）。
	Root string
	// EnvAllow 是允许透传的宿主环境变量名（精确匹配，大小写敏感）。
	EnvAllow []string
	// EnvSet 是额外注入/覆盖的变量（优先于透传）。
	EnvSet map[string]string
	// Timeout 到点先 TERM（KillGrace）后 KILL；0 = 不限。
	Timeout time.Duration
	// KillGrace 是 TERM→KILL 的宽限；0 = 2s。
	KillGrace time.Duration

	// Limits 是资源上限（0 = 不限）。通过宿主 prlimit(1) 施加。
	// **缺失 prlimit 时拒绝启动**（fail-closed）——不静默降级成"没有限制还在报告里
	// 说限制了"，那是最危险的谎。
	Limits Rlimits
	// NetIsolate 要求无网络命名空间。通过宿主 unshare(1) 施加（Linux，通常需特权）。
	// 缺失或无权时拒绝启动，不假装已隔离。
	NetIsolate bool
}

// Rlimits 是子进程资源上限（0 字段 = 该项不限）。单位见字段注释。
type Rlimits struct {
	CPU    time.Duration // CPU 时间（RLIMIT_CPU，换算成整秒）
	AS     int64         // 地址空间字节数（RLIMIT_AS）
	NOFILE int           // 打开文件数（RLIMIT_NOFILE）
	NPROC  int           // 进程/线程数（RLIMIT_NPROC）
}

// IsZero 报告是否一项都没设（此时不引入任何 wrapper）。
func (r Rlimits) IsZero() bool {
	return r.CPU <= 0 && r.AS <= 0 && r.NOFILE <= 0 && r.NPROC <= 0
}

// Flags 渲染成 prlimit 参数（不包含 `--` 与命令本身）。
func (r Rlimits) Flags() []string {
	var f []string
	if r.CPU > 0 {
		f = append(f, fmt.Sprintf("--cpu=%d", int(r.CPU.Seconds()+0.5)))
	}
	if r.AS > 0 {
		f = append(f, fmt.Sprintf("--as=%d", r.AS))
	}
	if r.NOFILE > 0 {
		f = append(f, fmt.Sprintf("--nofile=%d", r.NOFILE))
	}
	if r.NPROC > 0 {
		f = append(f, fmt.Sprintf("--nproc=%d", r.NPROC))
	}
	return f
}

// Result 是一次执行的结果。
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	TimedOut bool
	// Killed 报告是否由沙箱主动杀（超时/取消）。
	Killed bool
	// Wrapped 列出实际套上的隔离手段（如 "net:unshare -n" / "rlimit:prlimit --cpu=5"）。
	// 空 = 本次执行没有任何额外隔离 —— 证据里能看出真做了什么，不靠推测。
	Wrapped []string
}

// ErrBinaryNotFound 表示可执行文件不在 PATH（fail-closed，不进 shell 猜）。
var ErrBinaryNotFound = errors.New("sandbox: 可执行文件不在 PATH")

// ErrRootNotDir 表示 jail 根不是目录。
var ErrRootNotDir = errors.New("sandbox: jail 根不是目录")

// ErrLimitsUnsupported 表示要求了 RLIMIT 但宿主没有 prlimit(1)（fail-closed）。
var ErrLimitsUnsupported = errors.New("sandbox: 宿主不支持施加 RLIMIT")

// ErrNetIsolateUnsupported 表示要求了网络隔离但宿主没有 unshare(1)（fail-closed）。
var ErrNetIsolateUnsupported = errors.New("sandbox: 宿主不支持网络隔离")

// Apply 把围栏策略应用到一条已构造的 exec.Cmd 上（不负责启动/等待）。
//
// 给"自己管生命周期"的调用方复用同一套围栏语义（如 internal/proc 的进程管理器：
// 它负责登记/并发/超时/回收，围栏语义仍由此处单一来源提供，避免两套漂移）。
//
// 做的事：确认二进制可解析（fail-closed）、钉 cwd 到 jail 根、按白名单重建 env、
// 让子进程独立成组（调用方才能杀整组），以及按需套上 prlimit/unshare wrapper。
// 返回 wrapped（实际套上的隔离手段，供证据/日志），空 = 没套任何 wrapper。
func Apply(cmd *exec.Cmd, p Policy) (wrapped []string, err error) {
	if cmd == nil {
		return nil, errors.New("sandbox: cmd 为 nil")
	}
	if cmd.Err != nil {
		// exec.Command 构造期就 LookPath 过；失败在此如实反映，不进 shell 猜。
		return nil, fmt.Errorf("%w: %v", ErrBinaryNotFound, cmd.Err)
	}
	if p.Root != "" {
		st, stErr := os.Stat(p.Root)
		if stErr != nil {
			return nil, fmt.Errorf("sandbox: jail 根不可用: %w", stErr)
		}
		if !st.IsDir() {
			return nil, ErrRootNotDir
		}
		cmd.Dir = p.Root
	}
	cmd.Env = buildEnv(p)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if p.NetIsolate || !p.Limits.IsZero() {
		path, argv, kinds, wErr := wrapForIsolation(cmd.Path, cmd.Args[1:], p)
		if wErr != nil {
			return nil, wErr
		}
		cmd.Path, cmd.Args = path, argv
		wrapped = kinds
	}
	return wrapped, nil
}

// wrapForIsolation 按需把命令包进 unshare(1) / prlimit(1)。
//
// 顺序：unshare 在最外层（先建命名空间），prlimit 在其内。参数顺序即 argv 顺序。
// 找不到工具就**拒绝**：宁可启动失败，也不要静默地跑一个"没有隔离却报告说隔离了"
// 的进程（这类谎比没有隔离更危险）。
func wrapForIsolation(bin string, args []string, p Policy) (path string, argv []string, kinds []string, err error) {
	path = bin
	argv = append([]string{bin}, args...)
	if p.NetIsolate {
		up, lookErr := exec.LookPath("unshare")
		if lookErr != nil {
			return "", nil, nil, fmt.Errorf("%w: 需要 Linux 的 unshare(1)（当前宿主没有）", ErrNetIsolateUnsupported)
		}
		path = up
		argv = append([]string{up, "-n", "--"}, argv...)
		kinds = append(kinds, "net:unshare -n")
	}
	if !p.Limits.IsZero() {
		pp, lookErr := exec.LookPath("prlimit")
		if lookErr != nil {
			return "", nil, nil, fmt.Errorf("%w: 需要 util-linux 的 prlimit(1)（macOS 默认没有；"+
				"不设 Limits 即可运行）", ErrLimitsUnsupported)
		}
		flags := p.Limits.Flags()
		path = pp
		argv = append(append([]string{pp}, flags...), append([]string{"--"}, argv...)...)
		kinds = append(kinds, "rlimit:prlimit "+strings.Join(flags, " "))
	}
	return path, argv, kinds, nil
}

// Run 执行一条命令并等待其结束。
func Run(ctx context.Context, p Policy, name string, args ...string) (Result, error) {
	if strings.TrimSpace(name) == "" {
		return Result{}, errors.New("sandbox: 命令名不能为空")
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s", ErrBinaryNotFound, name)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil // 不继承父 stdin，避免子进程读交互输入卡死
	wrapped, err := Apply(cmd, p)
	if err != nil {
		return Result{}, err
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("sandbox: 启动失败: %w", err)
	}

	res := Result{Wrapped: wrapped}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	grace := p.KillGrace
	if grace <= 0 {
		grace = 2 * time.Second
	}

	var timeout <-chan time.Time
	if p.Timeout > 0 {
		t := time.NewTimer(p.Timeout)
		defer t.Stop()
		timeout = t.C
	}

	kill := func(sig syscall.Signal) {
		if cmd.Process == nil {
			return
		}
		// 负 pgid = 整个进程组
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}

	select {
	case err := <-done:
		res.Duration = time.Since(start)
		res.ExitCode = exitCodeOf(err)
	case <-ctx.Done():
		res.Killed = true
		kill(syscall.SIGKILL)
		<-done
		res.Duration = time.Since(start)
		res.ExitCode = exitCodeOf(ctx.Err())
	case <-timeout:
		res.TimedOut = true
		res.Killed = true
		kill(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(grace):
			kill(syscall.SIGKILL)
			<-done
		}
		res.Duration = time.Since(start)
		res.ExitCode = -1
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	return res, nil
}

// buildEnv 按白名单 + 注入集合构造子进程环境。
// 注意：必须返回非 nil 切片 —— Go 的 exec.Cmd 对 nil Env 会继承父进程环境，
// 只有非 nil（哪怕空）才表示"空环境"。这是"白名单为空 ⇒ 最严"的关键。
func buildEnv(p Policy) []string {
	out := make([]string, 0, len(p.EnvAllow)+len(p.EnvSet))
	for _, k := range p.EnvAllow {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range p.EnvSet {
		out = append(out, k+"="+v)
	}
	return out // 白名单为空 + 无注入 ⇒ 空环境（最严）
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// JailPath 把 p 解析到 root 之内；任何逃逸（..、绝对路径越界、符号链接外跳）
// 都返回 error（fail-closed）。
func JailPath(root, p string) (string, error) {
	if root == "" {
		return "", errors.New("sandbox: JailPath 需要非空 root")
	}
	if p == "" {
		return "", errors.New("sandbox: JailPath 需要非空路径")
	}
	if filepath.IsAbs(p) {
		// 绝对路径必须已在 root 内
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("sandbox: 绝对路径 %s 逃逸 jail 根 %s", p, root)
		}
		p = rel
	}
	joined := filepath.Join(root, p)
	cleanRoot := filepath.Clean(root)
	if !within(joined, cleanRoot) {
		return "", fmt.Errorf("sandbox: 路径 %s 逃逸 jail 根 %s", p, root)
	}
	// 尽力符号链接检查（不存在则跳过，由后续 open 报错）
	if real, err := filepath.EvalSymlinks(joined); err == nil {
		if realRoot, err2 := filepath.EvalSymlinks(cleanRoot); err2 == nil {
			if !within(real, realRoot) {
				return "", fmt.Errorf("sandbox: 符号链接 %s 逃逸 jail 根", p)
			}
		}
	}
	return joined, nil
}

func within(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// Fence 是一个可复用的围栏（策略 + 便捷方法）。
type Fence struct {
	Policy Policy
}

// NewFence 用策略造一个围栏。
func NewFence(p Policy) *Fence { return &Fence{Policy: p} }

// Run 同包级 Run。
func (f *Fence) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return Run(ctx, f.Policy, name, args...)
}

// Resolve 把 jail 内相对路径解析为可安全使用的绝对路径。
func (f *Fence) Resolve(p string) (string, error) { return JailPath(f.Policy.Root, p) }
