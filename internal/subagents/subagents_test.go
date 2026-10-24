package subagents

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSpawnListResultStop(t *testing.T) {
	release := make(chan struct{})
	m := NewManager(1, func(ctx context.Context, _ *Task) (string, error) {
		<-release
		return "done", nil
	})
	if _, err := m.Spawn("a", "goal"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Spawn("b", "goal"); err == nil {
		t.Fatal("超过并发上限应拒绝")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if m.Running() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("子智能体未结束")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("任务数 = %d", got)
	}
	id := m.List()[0].ID
	task, err := m.Result(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" || task.Answer != "done" {
		t.Fatalf("任务结果: %+v", task)
	}
}

func TestStopCancelsRunning(t *testing.T) {
	m := NewManager(2, func(ctx context.Context, _ *Task) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	task, err := m.Spawn("a", "goal")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Running() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("停止后任务未退出")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := m.Result(task.ID)
	if got.Status != "stopped" {
		t.Fatalf("状态 = %s", got.Status)
	}
}

func TestRunnerError(t *testing.T) {
	m := NewManager(1, func(context.Context, *Task) (string, error) {
		return "", errors.New("boom")
	})
	task, err := m.Spawn("a", "goal")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Running() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("任务未结束")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := m.Result(task.ID)
	if got.Status != "error" || got.Error != "boom" {
		t.Fatalf("错误状态: %+v", got)
	}
}
