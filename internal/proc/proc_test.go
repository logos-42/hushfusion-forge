package proc

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestProcHelper 是本包的辅助进程：被 Manager 用 os.Args[0] 自重exec 拉起。
// 不处于辅助模式时直接跳过（普通 `go test` 不会命中它的断言）。
func TestProcHelper(t *testing.T) {
	switch os.Getenv("PROC_HELPER") {
	case "":
		t.Skip("仅作为辅助进程被拉起")
	case "exit3":
		os.Exit(3)
	case "sleep":
		d, _ := time.ParseDuration(os.Getenv("PROC_HELPER_DUR"))
		time.Sleep(d)
	}
}

// helperOpts 造一份"拉起测试二进制自己"的启动选项。
func helperOpts(mode string, dur time.Duration) Options {
	return Options{Env: []string{
		"PROC_HELPER=" + mode,
		"PROC_HELPER_DUR=" + dur.String(),
	}}
}

func selfArgs() (string, []string) {
	return os.Args[0], []string{"-test.run=TestProcHelper"}
}

func TestStartRecordsAndReapsExitCode(t *testing.T) {
	m := NewManager(0)
	name, args := selfArgs()
	p, err := m.Start(context.Background(), "p-exit", name, args, helperOpts("exit3", 0))
	if err != nil {
		t.Fatal(err)
	}
	if p.PID <= 0 {
		t.Fatalf("PID 应已分配, got %d", p.PID)
	}
	if err := m.Wait("p-exit"); err != nil {
		t.Fatal(err)
	}
	in, ok := m.Get("p-exit")
	if !ok {
		t.Fatal("进程应在表内")
	}
	if in.State != StateFailed || in.ExitCode != 3 {
		t.Fatalf("应为 Failed/exit=3, got %s/%d", in.State, in.ExitCode)
	}
	if m.Running() != 0 {
		t.Fatalf("结束后不应有运行中进程, got %d", m.Running())
	}
}

func TestStopKillsProcessGroup(t *testing.T) {
	m := NewManager(0)
	name, args := selfArgs()
	start := time.Now()
	if _, err := m.Start(context.Background(), "p-sleep", name, args, helperOpts("sleep", 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if m.Running() != 1 {
		t.Fatalf("应有 1 个运行中, got %d", m.Running())
	}
	if err := m.Stop("p-sleep"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait("p-sleep"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Stop 应立即生效, took %v", d)
	}
	in, _ := m.Get("p-sleep")
	if in.State != StateKilled {
		t.Fatalf("Stop 过的进程应为 Killed, got %s", in.State)
	}
}

func TestStopKillsOrphanedGrandchildren(t *testing.T) {
	m := NewManager(0)
	start := time.Now()
	// sh 再 fork 一个 sleep 30：杀进程组必须把孙子也带走
	if _, err := m.Start(context.Background(), "p-sh", "sh",
		[]string{"-c", "sleep 30 & sleep 30"}, Options{}); err != nil {
		t.Skipf("缺 sh: %v", err)
	}
	if err := m.Stop("p-sh"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wait("p-sh"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("孙进程应随组被杀, took %v", d)
	}
}

func TestConcurrencyCapFailsClosed(t *testing.T) {
	m := NewManager(1)
	name, args := selfArgs()
	if _, err := m.Start(context.Background(), "c1", name, args, helperOpts("sleep", 5*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := m.Start(context.Background(), "c2", name, args, helperOpts("sleep", 5*time.Second))
	if !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("并发满应 fail-closed, got %v", err)
	}
	_ = m.Stop("c1")
	_ = m.Wait("c1")
}

func TestDuplicateRunningIDRejected(t *testing.T) {
	m := NewManager(0)
	name, args := selfArgs()
	if _, err := m.Start(context.Background(), "dup", name, args, helperOpts("sleep", 5*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := m.Start(context.Background(), "dup", name, args, helperOpts("sleep", 5*time.Second))
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("重复运行中 id 应被拒, got %v", err)
	}
	_ = m.Stop("dup")
	_ = m.Wait("dup")
}

func TestStopAllAndListOrder(t *testing.T) {
	m := NewManager(0)
	name, args := selfArgs()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := m.Start(context.Background(), id, name, args, helperOpts("sleep", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	list := m.List()
	if len(list) != 3 || list[0].ID != "a" || list[2].ID != "c" {
		t.Fatalf("List 应按启动顺序: %+v", list)
	}
	if n := m.StopAll(); n != 3 {
		t.Fatalf("StopAll 应停 3 个, got %d", n)
	}
	for _, id := range []string{"a", "b", "c"} {
		_ = m.Wait(id)
	}
	if m.Running() != 0 {
		t.Fatalf("全停后应无运行中, got %d", m.Running())
	}
	if n := m.Shutdown(); n != 0 {
		t.Fatalf("Shutdown 应无需再停, got %d", n)
	}
	if _, err := m.Start(context.Background(), "after", name, args, helperOpts("sleep", time.Second)); err == nil {
		t.Fatal("Shutdown 后 Start 应被拒")
	}
}

func TestStopUnknownAndEmptyArgs(t *testing.T) {
	m := NewManager(0)
	if err := m.Stop("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应 ErrNotFound, got %v", err)
	}
	if _, err := m.Start(context.Background(), "", "x", nil, Options{}); err == nil {
		t.Fatal("空 id 应被拒")
	}
	if _, err := m.Start(context.Background(), "ok", "  ", nil, Options{}); err == nil {
		t.Fatal("空命令名应被拒")
	}
}

func TestWaitUnknown(t *testing.T) {
	m := NewManager(0)
	if err := m.Wait("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应 ErrNotFound, got %v", err)
	}
}
