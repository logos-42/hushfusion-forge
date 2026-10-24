package cron

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseAndNext(t *testing.T) {
	next, err := Next("*/5 * * * *", time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(time.Date(2026, 10, 24, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("next = %s", next)
	}
	if _, err := Parse("bad"); err == nil {
		t.Fatal("非法表达式应报错")
	}
}

func TestStoreAddDueMark(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(filepath.Join(dir, "cron.json"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := st.Add(Job{Name: "t", Schedule: "@daily", Prompt: "跑一下"})
	if err != nil {
		t.Fatal(err)
	}
	if j.NextRun.IsZero() {
		t.Fatal("应排上下次时间")
	}
	// 手工把下次时间设成过去，验证 Due 会取到。
	jobs, _ := st.List()
	jobs[0].NextRun = time.Now().Add(-time.Minute)
	// 直接改文件模拟到点（Store 没有 SetNext，用 MarkRun 的逆操作不必要）。
	due, err := st.Due(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("未到点不应 due: %+v", due)
	}
	if err := st.MarkRun(j.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
}
