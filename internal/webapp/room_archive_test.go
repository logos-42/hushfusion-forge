package webapp

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoomArchive(t *testing.T) {
	s, err := NewWithRoot("", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.http.Handler)
	defer ts.Close()

	s.postRoom(Agent{ID: "a", Name: "A"}, "t1", "sess-1", "discuss", "第一条")
	s.postRoom(Agent{ID: "b", Name: "B"}, "t1", "sess-1", "discuss", "第二条")

	out, err := s.roomArchive()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "+2 messages") {
		t.Fatalf("首次存档输出 = %q", out)
	}
	journal := filepath.Join(s.dir, "room-archive", "journal.jsonl")
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "\n"); got != 2 {
		t.Fatalf("journal 行数 = %d, want 2", got)
	}
	if logOut, err := runGit(filepath.Join(s.dir, "room-archive"), "log", "--oneline"); err != nil || !strings.Contains(logOut, "room archive") {
		t.Fatalf("git log = %q err=%v", logOut, err)
	}

	// 无新增 → 跳过。
	if out, err := s.roomArchive(); err != nil || !strings.Contains(out, "无新增") {
		t.Fatalf("二次存档 = %q err=%v", out, err)
	}

	// 新增一条 → 增量 +1。
	s.postRoom(Agent{ID: "a", Name: "A"}, "t1", "sess-1", "discuss", "第三条")
	if out, err := s.roomArchive(); err != nil || !strings.Contains(out, "+1 messages") {
		t.Fatalf("增量存档 = %q err=%v", out, err)
	}
}
