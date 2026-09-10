package sync

import (
	"os"
	"path/filepath"
	"strings"
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

// newManualCtrl 构造一个不启动 loop 的 Ctrl：便于确定性地直接驱动 align / ConfirmDeletes。
func newManualCtrl(t *testing.T, lm watch.ListManyFunc, xf Transferer) (*Ctrl, config.SyncRule, string) {
	t.Helper()
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "rule1", Name: "t", Host: "h", Kind: "dir",
		RemotePath: "/r", LocalPath: local, MaxDepth: 0,
		PollIntervalS: 1, ForcePoll: true, Enabled: true, MirrorDelete: true,
	}
	rule.Normalize()
	c := NewCtrl(Deps{
		ListMany: lm, Transfer: xf, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { return nil },
	})
	return c, rule, local
}

// attachRuntime 手工登记一个规则运行时（不启动 goroutine），并返回它。
func attachRuntime(t *testing.T, c *Ctrl, rule config.SyncRule) *ruleRuntime {
	t.Helper()
	st := NewStateStore(filepath.Join(c.d.StateDir, "sync-"+rule.ID+".json"), c.fingerprintOf(rule))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	r := &ruleRuntime{
		rule: rule, state: st, status: "connected",
		cancel: make(chan struct{}), queue: map[string]watch.Kind{}, wake: make(chan struct{}, 1),
	}
	c.mu.Lock()
	c.run[rule.ID] = r
	c.mu.Unlock()
	return r
}

// keepEntry 是一条与远端一致的基线条目（远端未变 ⇒ 不会被判为变化/重新下载）。
func keepEntry() *Entry {
	return &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00", LocalSize: 3, LocalMTime: "2026-09-10 10:00", HasLocal: true}
}

func waitConflict(t *testing.T, c *Ctrl, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Conflicts(id)) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 %d 个冲突超时", n)
}

func waitFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && string(b) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	t.Fatalf("%s 内容未变成 %q，实际 %q", path, want, string(b))
}

// Critical 1：全量对账的删除路径必须和事件路径一样保护"本地被用户改过"的文件：
// 远端已删 + 本地大小/mtime 与基线不同 ⇒ 绝不删除，条目保留。
func TestEngineReconcileKeepsLocallyModifiedFile(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {
			{Name: "keep1.txt", Size: 3, ModTime: "2026-09-10 10:00"},
			{Name: "keep2.txt", Size: 3, ModTime: "2026-09-10 10:00"},
		},
	}}
	c, rule, local := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)
	r.state.With(func(d *StateFile) {
		d.Entries["keep1.txt"] = keepEntry()
		d.Entries["keep2.txt"] = keepEntry()
		d.Entries["gone.txt"] = &Entry{RemoteSize: 4, RemoteMTime: "2026-09-10 10:00", LocalSize: 4, LocalMTime: "BASELINE", HasLocal: true}
	})
	if err := os.WriteFile(filepath.Join(local, "gone.txt"), []byte("modified locally!!"), 0644); err != nil {
		t.Fatal(err)
	}

	c.align(r)

	if _, err := os.Stat(filepath.Join(local, "gone.txt")); err != nil {
		t.Fatalf("被本地修改过的文件遭对账删除: %v", err)
	}
	present := false
	r.state.With(func(d *StateFile) { present = d.Entries["gone.txt"] != nil })
	if !present {
		t.Fatal("保留的文件不得从状态条目里消失")
	}
}

// Minor 6（随 Critical 1 折叠）：删除动作失败时绝不能顺手把条目删掉，否则该路径永久成孤儿。
func TestEngineReconcileKeepsEntryWhenRemovalFails(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {
			{Name: "keep1.txt", Size: 3, ModTime: "2026-09-10 10:00"},
			{Name: "keep2.txt", Size: 3, ModTime: "2026-09-10 10:00"},
			{Name: "keep3.txt", Size: 3, ModTime: "2026-09-10 10:00"},
		},
	}}
	c, rule, local := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)

	// 非空目录：os.Remove 必然失败（ENOTEMPTY），用来验证"删除失败不丢条目"。
	dir := filepath.Join(local, "goneDir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := osStat(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["keep1.txt"] = keepEntry()
		d.Entries["keep2.txt"] = keepEntry()
		d.Entries["keep3.txt"] = keepEntry()
		// 基线取自当前 stat ⇒ keep 判定不成立，删除会真的执行并因目录非空而失败。
		d.Entries["goneDir"] = &Entry{RemoteSize: 1, RemoteMTime: "2026-09-10 10:00", LocalSize: st.Size, LocalMTime: st.ModTime, HasLocal: true}
	})

	c.align(r)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("删除失败的目录不应消失: %v", err)
	}
	present := false
	r.state.With(func(d *StateFile) { present = d.Entries["goneDir"] != nil })
	if !present {
		t.Fatal("删除失败后条目必须保留，否则该路径永久成孤儿")
	}
}

// Important 2 take_remote：裁决必须被真正执行，而不是对同一冲突状态重跑 Decide（只会再判冲突）。
func TestEngineResolveTakeRemoteTransfers(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r":       {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
		"/r/a.txt": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
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

	waitConflict(t, c, rule.ID, 1)
	st, _ := osStat(filepath.Join(local, "a.txt"))
	if err := c.ResolveConflict(rule.ID, "a.txt", ConflictTakeRemote, st); err != nil {
		t.Fatalf("resolve take_remote: %v", err)
	}
	waitFileContent(t, filepath.Join(local, "a.txt"), "hello")
	time.Sleep(300 * time.Millisecond)
	if cs := c.Conflicts(rule.ID); len(cs) != 0 {
		t.Fatalf("take_remote 后冲突不得复活: %+v", cs)
	}
}

// Important 2 save_as：远端版本另存为兄弟文件，本地原文件原样保留，冲突不复活。
func TestEngineResolveSaveAsWritesSibling(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r":       {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
		"/r/a.txt": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
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

	waitConflict(t, c, rule.ID, 1)
	st, _ := osStat(filepath.Join(local, "a.txt"))
	if err := c.ResolveConflict(rule.ID, "a.txt", ConflictSaveAs, st); err != nil {
		t.Fatalf("resolve save_as: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	sibling := ""
	for time.Now().Before(deadline) && sibling == "" {
		entries, _ := os.ReadDir(local)
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "a.txt.remote-") {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(local, e.Name())); err == nil && string(b) == "hello" {
				sibling = e.Name()
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sibling == "" {
		t.Fatal("save_as 未写出远端副本兄弟文件")
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(b) != "much longer local content" {
		t.Fatal("save_as 绝不能改写本地原文件")
	}
	time.Sleep(300 * time.Millisecond)
	if cs := c.Conflicts(rule.ID); len(cs) != 0 {
		t.Fatalf("save_as 后冲突不得复活: %+v", cs)
	}
}

// Important 3：fetchMeta 是锁外网络往返，期间并发对账可能换掉删除清单；
// 删除前必须重新校验指纹，只删用户确认过的那一批。
func TestEngineConfirmDeletesRevalidatesFingerprint(t *testing.T) {
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "rule1", Name: "t", Host: "h", Kind: "dir",
		RemotePath: "/r", LocalPath: local, MaxDepth: 0,
		PollIntervalS: 1, ForcePoll: true, Enabled: true, MirrorDelete: true,
	}
	rule.Normalize()
	var rr *ruleRuntime
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	// ConfirmDeletes 会经 fetchMeta 调用 ListMany；此刻模拟一次并发对账换掉指纹。
	mutating := func(host, user string, paths []string) (map[string][]sftp.Item, error) {
		if rr != nil {
			rr.mu.Lock()
			rr.delFP = "changed-by-concurrent-align"
			rr.mu.Unlock()
		}
		return lm.list(host, user, paths)
	}
	c := NewCtrl(Deps{ListMany: mutating, Transfer: &fakeXfer{remote: map[string]string{}}, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { return nil }})
	r := attachRuntime(t, c, rule)
	rr = r

	if err := os.WriteFile(filepath.Join(local, "gone.txt"), []byte("doomed"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := osStat(filepath.Join(local, "gone.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// 基线取当前 stat ⇒ keep 判定不成立；若不重新校验指纹，这个文件会被真的删掉。
	r.state.With(func(d *StateFile) {
		d.Entries["gone.txt"] = &Entry{RemoteSize: st.Size, RemoteMTime: "2026-09-10 10:00", LocalSize: st.Size, LocalMTime: st.ModTime, HasLocal: true}
	})
	r.mu.Lock()
	r.pendingDel = []string{"gone.txt"}
	r.delFP = "fp-1"
	r.mu.Unlock()

	if err := c.ConfirmDeletes(rule.ID, "fp-1"); err == nil {
		t.Fatal("指纹在 fetch 期间变化，确认必须失效，不能照删")
	}
	if _, err := os.Stat(filepath.Join(local, "gone.txt")); err != nil {
		t.Fatalf("确认已过期时绝不能执行删除: %v", err)
	}
}

// Stop 必须幂等：对未运行的规则调用 Stop 返回 nil，而不是错误。契约与
// forward.Ctrl.Stop 对齐（进程不存在时 return nil），否则上层无法用它清 Enabled。
func TestEngineStopIsIdempotentWhenNotRunning(t *testing.T) {
	c, rule, _ := newTestCtrl(t, nil, nil)
	if err := c.Stop(rule.ID); err != nil {
		t.Fatalf("未运行的规则 Stop 必须返回 nil（幂等契约），得到 %v", err)
	}
	// 再调一次也必须 nil：重复 Stop 不得改变契约。
	if err := c.Stop(rule.ID); err != nil {
		t.Fatalf("重复 Stop 必须返回 nil，得到 %v", err)
	}
}
