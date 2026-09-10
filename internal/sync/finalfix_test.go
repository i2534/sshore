package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// newFileCtrl 构造 kind=file 规则的引擎。After 立即返回，避免下载失败时
// 退避 select 永久阻塞（测试不需要真实退避）。
func newFileCtrl(t *testing.T, lm watch.ListManyFunc, xf Transferer) (*Ctrl, config.SyncRule, string) {
	t.Helper()
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "rule-file", Name: "f", Host: "h", Kind: "file",
		RemotePath: "/srv/conf/a.conf", LocalPath: local,
		PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()
	c := NewCtrl(Deps{
		ListMany: lm, Transfer: xf, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { ch := make(chan struct{}); close(ch); return ch },
	})
	return c, rule, local
}

// C1(a): kind=file 的首轮对齐必须真的下载那一个文件。
// 缺陷：applyAction 把 rel=basename 又 Join 到 RemotePath，请求了 /a.conf/a.conf。
func TestFileKindFirstDownload(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	waitFileContent(t, filepath.Join(local, "a.conf"), "hello")
}

// C1(b): 远端单文件变化后必须被重新下载。
func TestFileKindRedownloadsOnRemoteChange(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	waitFileContent(t, filepath.Join(local, "a.conf"), "hello")

	xf.mu.Lock()
	xf.remote["/srv/conf/a.conf"] = "hello world"
	xf.mu.Unlock()
	lm.set("/srv/conf/a.conf", sftp.Item{Name: "a.conf", Size: 11, ModTime: "2026-09-10 11:00"})

	waitFileContent(t, filepath.Join(local, "a.conf"), "hello world")
}

// C1(c): 远端源文件消失时本地副本必须原样保留（§7.9：不受 mirror_delete 影响）。
func TestFileKindKeepsLocalWhenRemoteDisappears(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	waitFileContent(t, filepath.Join(local, "a.conf"), "hello")

	lm.mu.Lock()
	delete(lm.tree, "/srv/conf/a.conf")
	lm.mu.Unlock()

	// 至少跨过一个轮询周期，让 delete 事件走完事件路径。
	time.Sleep(2 * time.Second)
	b, err := os.ReadFile(filepath.Join(local, "a.conf"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("远端源消失后本地副本必须保留，err=%v content=%q", err, string(b))
	}
}

// newEventCtrl 构造一个不启动 loop 的引擎运行时，供手工驱动 consume 的事件路径。
// After 立即返回，保证失败的下载也会快速返回而不是挂在退避 select 上。
func newEventCtrl(t *testing.T, rule config.SyncRule, lm watch.ListManyFunc, xf Transferer) (*Ctrl, *ruleRuntime) {
	t.Helper()
	c := NewCtrl(Deps{
		ListMany: lm, Transfer: xf, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { ch := make(chan struct{}); close(ch); return ch },
	})
	r := attachRuntime(t, c, rule)
	return c, r
}

// driveEvent 把一条事件送进真实的 consume（入队 → 300ms 去抖 → drainQueue），
// 等待去抖窗口后关闭通道并等待 consume 返回。
func driveEvent(t *testing.T, c *Ctrl, r *ruleRuntime, ev watch.Event) {
	t.Helper()
	ch := make(chan watch.Event, 4)
	done := make(chan bool, 1)
	go func() { done <- c.consume(r, ch) }()
	ch <- ev
	time.Sleep(600 * time.Millisecond)
	close(ch)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("consume 未在通道关闭后返回")
	}
}

func dirRule(local string) config.SyncRule {
	rule := config.SyncRule{
		ID: "r", Host: "h", Kind: "dir", RemotePath: "/r", LocalPath: local,
		MaxDepth: 0, PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()
	return rule
}

// C2(a): 被排除路径的文件事件既不得触发下载，也不得被采纳进基线。
func TestEventPathDropsExcludedPath(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local) // Normalize 会灌入 DefaultExcludes（含 node_modules/）
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r/node_modules/x.js": {{Name: "x.js", Size: 3, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/node_modules/x.js": "abc"}}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	driveEvent(t, c, r, watch.Event{RelPath: "node_modules/x.js", Kind: watch.KindWrite})

	if xf.calls != 0 {
		t.Fatalf("被排除路径的事件不得触发下载，calls=%d", xf.calls)
	}
	present := false
	r.state.With(func(d *StateFile) { present = d.Entries["node_modules/x.js"] != nil })
	if present {
		t.Fatal("被排除路径不得被采纳/登记进基线")
	}
}

// C2(b): 一个被排除路径一旦进入基线，对账会把它当成"远端已删"而删掉本地文件。
// 修复后事件在队列边界被丢弃，本地文件必须原样保留。
func TestEventPathExcludedPathNotDeletedByReconcile(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	rule.MirrorDelete = true

	if err := os.MkdirAll(filepath.Join(local, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	// 用户原本就存在的本地文件，大小与远端元信息一致（会被判为"采纳"）。
	if err := os.WriteFile(filepath.Join(local, "node_modules", "x.js"), []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}

	tree := map[string][]sftp.Item{}
	keep := make([]sftp.Item, 0, 12)
	for i := 0; i < 12; i++ {
		keep = append(keep, sftp.Item{Name: fmt.Sprintf("keep%02d.txt", i), Size: 3, ModTime: "2026-09-10 10:00"})
	}
	tree["/r"] = keep
	tree["/r/node_modules/x.js"] = []sftp.Item{{Name: "x.js", Size: 3, ModTime: "2026-09-10 10:00"}}
	lm := &scriptedListMany{tree: tree}
	xf := &fakeXfer{remote: map[string]string{"/r/node_modules/x.js": "abc"}}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	// 预置 12 条与远端一致的基线：单个删除低于闸门阈值，会真的执行删除。
	r.state.With(func(d *StateFile) {
		for i := 0; i < 12; i++ {
			d.Entries[fmt.Sprintf("keep%02d.txt", i)] = &Entry{
				RemoteSize: 3, RemoteMTime: "2026-09-10 10:00",
				LocalSize: 3, LocalMTime: "2026-09-10 10:00", HasLocal: true,
			}
		}
	})

	driveEvent(t, c, r, watch.Event{RelPath: "node_modules/x.js", Kind: watch.KindWrite})
	c.align(r)

	if _, err := os.Lstat(filepath.Join(local, "node_modules", "x.js")); err != nil {
		t.Fatalf("被排除的本地文件被对账路径删除: %v", err)
	}
}

// C2(c): 超出 max_depth 的路径事件必须被队列边界过滤。
func TestEventPathDropsPathBeyondMaxDepth(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local) // MaxDepth=0：仅本层
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r/deep/very/deep.txt": {{Name: "deep.txt", Size: 3, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/deep/very/deep.txt": "abc"}}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	driveEvent(t, c, r, watch.Event{RelPath: "deep/very/deep.txt", Kind: watch.KindWrite})

	if xf.calls != 0 {
		t.Fatalf("超出 max_depth 的事件不得触发下载，calls=%d", xf.calls)
	}
	present := false
	r.state.With(func(d *StateFile) { present = d.Entries["deep/very/deep.txt"] != nil })
	if present {
		t.Fatal("超出 max_depth 的路径不得进入基线")
	}
}

// I1: 事件路径下载成功后登记条目时必须写入刚取回的远端元信息，
// 否则下一次对账会因 Remote* 为空而把它判成"变化"，再下载一次。
func TestEventPathDownloadRecordsRemoteMetadata(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r/a.txt": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	driveEvent(t, c, r, watch.Event{RelPath: "a.txt", Kind: watch.KindWrite})
	if xf.calls != 1 {
		t.Fatalf("事件路径应下载一次，calls=%d", xf.calls)
	}

	var got Entry
	found := false
	r.state.With(func(d *StateFile) {
		if e := d.Entries["a.txt"]; e != nil {
			got = *e
			found = true
		}
	})
	if !found {
		t.Fatal("事件路径下载后必须登记条目")
	}
	if got.RemoteSize != 5 || got.RemoteMTime != "2026-09-10 10:00" {
		t.Fatalf("条目必须携带远端元信息，得到 RemoteSize=%d RemoteMTime=%q", got.RemoteSize, got.RemoteMTime)
	}

	// 元信息齐全时，随后的对账不得再下载同一个文件。
	c.align(r)
	if xf.calls != 1 {
		t.Fatalf("元信息齐全时对账不得二次下载，calls=%d", xf.calls)
	}
}

// I2（引擎侧）：fetchMeta 拿不到某路径的元信息（ent==nil）而本地已有同名文件时，
// 必须在引擎里产生冲突（不 panic、不下载覆盖）。
func TestEventPathUnknownMetaKeepsExistingLocalAsConflict(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	// ListMany 对所有路径都缺席：fetchMeta 拿不到 a.txt 的元信息。
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("local content"), 0644); err != nil {
		t.Fatal(err)
	}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	driveEvent(t, c, r, watch.Event{RelPath: "a.txt", Kind: watch.KindWrite})

	if xf.calls != 0 {
		t.Fatalf("远端元信息未知时不得下载覆盖本地文件，calls=%d", xf.calls)
	}
	if got := c.Conflicts(rule.ID); len(got) != 1 {
		t.Fatalf("未知元信息 + 本地已存在必须产生冲突，得到 %+v", got)
	}
}

// C2（完成）：对账的删除候选循环也必须跳过"不在处理范围"的路径。被 exclude 覆盖
// 的路径只是退出处理范围，不是"远端已删"——用户后来才加 exclude 时，绝不能因此
// 删掉已镜像的本地文件。同时保留语义：真正在范围内消失的路径仍会被删除。
func TestReconcileDoesNotDeleteNewlyExcludedPath(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	rule.MirrorDelete = true

	// 12 条 keep 基线，使本轮删除数低于闸门阈值（否则只会挂起等待确认）。
	keep := make([]sftp.Item, 0, 12)
	for i := 0; i < 12; i++ {
		keep = append(keep, sftp.Item{Name: fmt.Sprintf("keep%02d.txt", i), Size: 3, ModTime: "2026-09-10 10:00"})
	}
	lm := &scriptedListMany{tree: map[string][]sftp.Item{"/r": keep}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	writeBaselineFile := func(rel string) (LocalState, string) {
		t.Helper()
		target := filepath.Join(local, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("abc"), 0644); err != nil {
			t.Fatal(err)
		}
		st, err := osStat(target)
		if err != nil {
			t.Fatal(err)
		}
		return st, target
	}

	excludedSt, excludedTarget := writeBaselineFile("node_modules/x.js") // 默认 exclude 覆盖
	goneSt, goneTarget := writeBaselineFile("gone.txt")                  // 在范围内、远端真的消失

	r.state.With(func(d *StateFile) {
		for i := 0; i < 12; i++ {
			d.Entries[fmt.Sprintf("keep%02d.txt", i)] = &Entry{
				RemoteSize: 3, RemoteMTime: "2026-09-10 10:00",
				LocalSize: 3, LocalMTime: "2026-09-10 10:00", HasLocal: true,
			}
		}
		d.Entries["node_modules/x.js"] = &Entry{
			RemoteSize: excludedSt.Size, RemoteMTime: "2026-09-10 10:00",
			LocalSize: excludedSt.Size, LocalMTime: excludedSt.ModTime, HasLocal: true,
		}
		d.Entries["gone.txt"] = &Entry{
			RemoteSize: goneSt.Size, RemoteMTime: "2026-09-10 10:00",
			LocalSize: goneSt.Size, LocalMTime: goneSt.ModTime, HasLocal: true,
		}
	})

	c.align(r)

	if _, err := os.Stat(excludedTarget); err != nil {
		t.Fatalf("新加入 exclude 的已镜像路径被对账删除: %v", err)
	}
	present := false
	r.state.With(func(d *StateFile) { present = d.Entries["node_modules/x.js"] != nil })
	if !present {
		t.Fatal("被排除路径的基线条目必须保留，不能被当成远端已删而清除")
	}

	// 语义护栏：在范围内且远端确实消失的路径，必须照常删除。
	if _, err := os.Stat(goneTarget); !os.IsNotExist(err) {
		t.Fatalf("在范围内真正消失的路径必须被镜像删除，stat err=%v", err)
	}
	gonePresent := false
	r.state.With(func(d *StateFile) { gonePresent = d.Entries["gone.txt"] != nil })
	if gonePresent {
		t.Fatal("真正被删除的路径，其条目必须一并清除")
	}
}

// I3: 本地 stat 返回"非不存在"的错误（此例为自指符号链接的 ELOOP）时，
// 必须跳过该路径，绝不能当成"本地不存在"而 re-download 覆盖本地文件。
func TestEventPathSkipsOnNonNotExistStatError(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r/a.txt": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	link := filepath.Join(local, "a.txt")
	if err := os.Symlink("a.txt", link); err != nil {
		t.Skipf("无法创建符号链接（环境不支持）: %v", err)
	}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	driveEvent(t, c, r, watch.Event{RelPath: "a.txt", Kind: watch.KindWrite})

	if xf.calls != 0 {
		t.Fatalf("本地状态不可读时不得下载覆盖，calls=%d", xf.calls)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("不可读的本地文件不得被 rename 覆盖，mode=%v err=%v", fi.Mode(), err)
	}
}
