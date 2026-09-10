package sync

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// scriptedListMany 返回固定远端内容；写入通过 set 修改。
type scriptedListMany struct {
	mu   sync.Mutex
	tree map[string][]sftp.Item
}

func (s *scriptedListMany) list(host, user string, paths []string) (map[string][]sftp.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := map[string][]sftp.Item{}
	for _, p := range paths {
		if v, ok := s.tree[p]; ok {
			res[p] = v
		}
	}
	return res, nil
}

func (s *scriptedListMany) set(dir string, items ...sftp.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tree[dir] = items
}

// fakeXfer 把"远端内容"落到本地，用于验证原子写与冲突规则。
type fakeXfer struct {
	mu     sync.Mutex
	remote map[string]string
	calls  int
}

func (f *fakeXfer) Get(host, user, remote, local string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	body, ok := f.remote[remote]
	if !ok {
		return os.ErrNotExist
	}
	return os.WriteFile(local, []byte(body), 0644)
}

func newTestCtrl(t *testing.T, lm watch.ListManyFunc, xf Transferer) (*Ctrl, config.SyncRule, string) {
	t.Helper()
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "rule1", Name: "t", Host: "h", Kind: "dir",
		RemotePath: "/r", LocalPath: local, MaxDepth: 0,
		PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()
	c := NewCtrl(Deps{
		ListMany: lm, Transfer: xf, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { return nil }, // 不自动重连，测试手动驱动
	})
	return c, rule, local
}

// 首轮对齐：远端已有文件必须被拉下来（决策 5 的"先全量对齐"）。
func TestEngineFirstRunAligns(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(local, "a.txt")); err == nil && string(b) == "hello" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("首轮对齐未把 a.txt 拉下来")
}

// 本地已存在且大小一致 ⇒ 采纳：登记基线但**不下载**（首轮不覆盖用户文件）。
func TestEngineFirstRunAdoptsSameSizeFile(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	time.Sleep(700 * time.Millisecond)
	if xf.calls != 0 {
		t.Fatalf("同大小文件不应被下载，实际下载 %d 次", xf.calls)
	}
	b, _ := os.ReadFile(filepath.Join(local, "a.txt"))
	if string(b) != "other" {
		t.Fatal("采纳分支绝不能改写本地文件")
	}
}

// 本地已存在但大小不同 ⇒ 冲突，绝不覆盖。
func TestEngineFirstRunConflictsOnDifferentSize(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Conflicts(rule.ID)) == 1 {
			if xf.calls != 0 {
				t.Fatalf("冲突分支绝不能下载，实际 %d 次", xf.calls)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("大小不同必须产生冲突")
}
