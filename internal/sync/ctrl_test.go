package sync

import (
	"fmt"
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
	// FIX 2b 适配：诚实地模拟"闸门确实以阈值臂挂起过本批"。否则新增的纵深防御
	// （DeleteNeedsConfirm=false 一律拒绝）会提前拦截，本用例就测不到指纹重校验了。
	r.stats.DeleteNeedsConfirm = true
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

// 端到端验证"镜像删除真的会删文件"。它能同时守住三件事：
//  1. applyOne 的 ActionDelete 会登记进 pendingDel；
//  2. drainQueue 处理完会真的调闸门（否则这里永远不会删）；
//  3. 闸门在事件路径（CountKnown=false）放行单条删除。
func TestEngineMirrorDeleteActuallyDeletes(t *testing.T) {
	fs := &scriptedListMany{tree: map[string][]sftp.Item{}}
	fs.set("/r", sftp.Item{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"})
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, fs.list, xf)
	rule.MirrorDelete = true
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	target := filepath.Join(local, "a.txt")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(target)
		return err == nil
	}, "首轮对齐未把 a.txt 拉下来")

	fs.set("/r") // 远端删除
	waitFor(t, 6*time.Second, func() bool {
		_, err := os.Stat(target)
		return os.IsNotExist(err)
	}, "mirror_delete 未删除本地文件（删除闸门没接上或未放行）")
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// M2：RetryFailed 重新入队失败项、清空失败计数与清单，并按 Forced 标记重放用户当初
// 的裁决（强制 take_remote→ActionGet、强制 save_as→ActionSaveAs）。刻意混入一条
// Forced=false 的普通下载，证明不会把 Decide 推导的动作误当成用户裁决。
// 用 newManualCtrl/attachRuntime 手动登记运行时（不启动 goroutine），因此不需要
// 300ms 去抖容差，也不是时序相关的。
func TestRetryFailedRequeuesClearsAndReplaysSaveAs(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, _ := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	r.state.With(func(d *StateFile) {
		d.Failed = []FailedItem{
			{RelPath: "a.txt", Err: "boom", At: "2026-09-10T10:00:00Z", Action: ActionGet},
			{RelPath: "b/c.txt", Err: "boom", At: "2026-09-10T10:00:01Z", Action: ActionGet, Forced: true},
			{RelPath: "secret.key", Err: "boom", At: "2026-09-10T10:00:02Z", Action: ActionSaveAs, Forced: true},
		}
	})
	r.mu.Lock()
	// 故意让计数大于失败条数：模拟「摘除 d.Failed 之后、更新 stats 之前引擎又记了新失败」。
	// 重试只应减去实际重排的 3 条（FIX 5），不能硬清零。
	r.stats.Failed = 5
	r.mu.Unlock()

	n, err := c.RetryFailed(rule.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n != 3 {
		t.Fatalf("应重试 3 条，得到 %d", n)
	}
	r.mu.Lock()
	gotA, gotB, gotC := r.queue["a.txt"], r.queue["b/c.txt"], r.queue["secret.key"]
	forcedGet := r.resolved["b/c.txt"]
	forcedSaveAs := r.resolved["secret.key"]
	_, ordinaryDownloadWasForced := r.resolved["a.txt"]
	failedStat := r.stats.Failed
	r.mu.Unlock()
	if gotA != watch.KindWrite || gotB != watch.KindWrite || gotC != watch.KindWrite {
		t.Fatalf("三条都应入队为 KindWrite，得到 %v %v %v", gotA, gotB, gotC)
	}
	if forcedGet != ActionGet {
		t.Fatalf("强制 take_remote（Forced 的 ActionGet）必须重放为 resolved=ActionGet，得到 %v", forcedGet)
	}
	if forcedSaveAs != ActionSaveAs {
		t.Fatalf("save_as 失败项必须重放为 resolved=ActionSaveAs，得到 %v", forcedSaveAs)
	}
	if ordinaryDownloadWasForced {
		t.Fatal("Forced=false 的普通下载不得写入 resolved（会被误当成用户裁决）")
	}
	if failedStat != 2 {
		t.Fatalf("应按实际重排条数扣减（5-3=2）而不是清零，得到 %d", failedStat)
	}
	r.state.With(func(d *StateFile) {
		if len(d.Failed) != 0 {
			t.Fatalf("重试后失败清单必须清空，得到 %#v", d.Failed)
		}
	})
}

func TestRetryFailedErrorsWhenNotRunning(t *testing.T) {
	c := NewCtrl(Deps{StateDir: t.TempDir()})
	if _, err := c.RetryFailed("nope"); err == nil {
		t.Fatal("规则未运行必须报错")
	}
}

// M3：冲突计数必须反映**当前仍挂起**的冲突数（spec §9.2 要求 >0 才显示按钮），
// 而不是只增不减的累加器 —— 否则最后一条裁决完，「冲突」按钮永远不消失。
func TestConflictStatReflectsPendingNotTotal(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"}
	})
	c.applyOne(r, "a.txt", watch.KindWrite, &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"})
	if got := c.Stats()[rule.ID].Conflicts; got != 1 {
		t.Fatalf("产生一条冲突后计数应为 1，得到 %d", got)
	}
	// 裁决 keep_local：冲突出队，计数必须归零。
	if err := c.ResolveConflict(rule.ID, "a.txt", ConflictKeepLocal, LocalState{Exists: true, Size: 24, ModTime: "local-t"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := c.Stats()[rule.ID].Conflicts; got != 0 {
		t.Fatalf("裁决后计数必须归零，得到 %d", got)
	}
}

// M3 配套：成功执行强制 take_remote（ActionGet）会 removeConflict，计数必须同步归零。
// 这是第三个冲突移除点（另两处是 ResolveConflict 与 save_as）；漏刷新会让「冲突」
// 按钮在背后已无冲突时仍然显示。
func TestConflictStatZeroAfterSuccessfulForcedGet(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "rem"}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"}
	})
	c.applyOne(r, "a.txt", watch.KindWrite, &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"})
	if got := c.Stats()[rule.ID].Conflicts; got != 1 {
		t.Fatalf("前置：应产生 1 条挂起冲突，得到 %d", got)
	}
	// 走强制通道执行 take_remote：下载成功后 removeConflict，计数必须同步下降。
	c.applyResolved(r, "a.txt", ActionGet)
	if got := c.Stats()[rule.ID].Conflicts; got != 0 {
		t.Fatalf("成功 take_remote 后冲突计数必须归零，得到 %d", got)
	}
	r.state.With(func(d *StateFile) {
		if len(d.Conflicts) != 0 {
			t.Fatalf("成功 take_remote 后状态文件里不应再有冲突，得到 %#v", d.Conflicts)
		}
	})
}

// M3 配套：成功 save_as 同样会 removeConflict，计数必须同步归零。
func TestConflictStatZeroAfterSuccessfulForcedSaveAs(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "rem"}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)

	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"}
	})
	c.applyOne(r, "a.txt", watch.KindWrite, &Entry{RemoteSize: 3, RemoteMTime: "2026-09-10 10:00"})
	if got := c.Stats()[rule.ID].Conflicts; got != 1 {
		t.Fatalf("前置：应产生 1 条挂起冲突，得到 %d", got)
	}
	c.applyResolved(r, "a.txt", ActionSaveAs)
	if got := c.Stats()[rule.ID].Conflicts; got != 0 {
		t.Fatalf("成功 save_as 后冲突计数必须归零，得到 %d", got)
	}
}

// 重启后统计必须从状态文件播种：失败清单还在盘上，Failed 就不能是 0，否则重试按钮
// 被隐藏、用户无从触发 RetryFailed；冲突计数同理。
func TestStartSeedsFailedAndConflictStatsFromStateFile(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, _ := newTestCtrl(t, lm.list, xf)

	st := NewStateStore(filepath.Join(c.d.StateDir, "sync-"+rule.ID+".json"), c.fingerprintOf(rule))
	st.With(func(d *StateFile) {
		d.Failed = []FailedItem{{RelPath: "a.txt", Err: "boom", At: "2026-09-10T10:00:00Z", Action: ActionGet}}
		d.Conflicts = []Conflict{{RelPath: "b.txt"}}
	})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}

	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	got := c.Stats()[rule.ID]
	if got.Failed != 1 {
		t.Fatalf("启动后 Failed 必须由状态文件播种为 1，得到 %d", got.Failed)
	}
	if got.Conflicts != 1 {
		t.Fatalf("启动后 Conflicts 必须由状态文件播种为 1，得到 %d", got.Conflicts)
	}
}

// M5（性质 1）：事件路径挂起的删除也必须带非空指纹，否则「确认删除」永远确认不了。
func TestEventPathDeleteSuspensionHasFingerprint(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	rule.MirrorDelete = true
	r := attachRuntime(t, c, rule)

	target := filepath.Join(local, "gone.txt")
	if err := os.WriteFile(target, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["gone.txt"] = &Entry{
			RemoteSize: st.Size(), RemoteMTime: "2026-09-10 10:00",
			LocalSize: st.Size(), LocalMTime: FormatModTime(st.ModTime()), HasLocal: true,
		}
	})
	// 事件路径：只说「远端删了」，remote 元信息未知。
	c.applyOne(r, "gone.txt", watch.KindDelete, nil)

	got := c.Stats()[rule.ID]
	if got.DeletePending != 1 {
		t.Fatalf("事件路径挂起删除数应为 1，得到 %d", got.DeletePending)
	}
	if got.DeleteFingerprint == "" {
		t.Fatal("事件路径挂起的删除必须带非空指纹")
	}
}

// M5（性质 2）：pendingDel 变化后，先前发出的指纹必须失效。
func TestDeleteFingerprintInvalidatedWhenPendingGrows(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	xf := &fakeXfer{remote: map[string]string{}}
	c, rule, local := newManualCtrl(t, lm.list, xf)
	rule.MirrorDelete = true
	r := attachRuntime(t, c, rule)

	seedDeletable := func(rel string) {
		target := filepath.Join(local, rel)
		if err := os.WriteFile(target, []byte("abc"), 0644); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		r.state.With(func(d *StateFile) {
			d.Entries[rel] = &Entry{RemoteSize: st.Size(), RemoteMTime: "t",
				LocalSize: st.Size(), LocalMTime: FormatModTime(st.ModTime()), HasLocal: true}
		})
	}
	seedDeletable("a.txt")
	c.applyOne(r, "a.txt", watch.KindDelete, nil)
	r.mu.Lock()
	// FIX 2b 适配：模拟闸门以阈值臂挂起本批；否则新增的纵深防御会先拒绝，测不到指纹失效。
	r.stats.DeleteNeedsConfirm = true
	first := r.stats.DeleteFingerprint
	r.mu.Unlock()
	if first == "" {
		t.Fatal("首轮指纹不应为空")
	}

	seedDeletable("b.txt")
	c.applyOne(r, "b.txt", watch.KindDelete, nil)
	r.mu.Lock()
	r.stats.DeleteNeedsConfirm = true // 追加后需重新过闸门才可确认
	second := r.stats.DeleteFingerprint
	r.mu.Unlock()
	if second == first {
		t.Fatal("pendingDel 变化后指纹必须失效")
	}
	// 旧指纹必须被 ConfirmDeletes 拒绝（不会误删新增的那一批）。
	if err := c.ConfirmDeletes(rule.ID, first); err == nil {
		t.Fatal("使用过期指纹确认必须被拒绝")
	}
}

// M5（FIX 3）：pendingDel 清空后必须把指纹一并清掉 —— 空集合不该有可确认的指纹。
func TestDeleteFingerprintClearedWhenPendingEmpty(t *testing.T) {
	c, rule, _ := newManualCtrl(t, nil, nil)
	r := attachRuntime(t, c, rule)

	r.mu.Lock()
	r.pendingDel = []string{"a.txt"}
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	if got := c.Stats()[rule.ID].DeleteFingerprint; got == "" {
		t.Fatal("非空挂起集必须有指纹")
	}

	r.mu.Lock()
	r.pendingDel = nil
	r.refreshDeleteFingerprint()
	r.mu.Unlock()
	got := c.Stats()[rule.ID]
	if got.DeleteFingerprint != "" {
		t.Fatalf("清空后 DeleteFingerprint 必须为空串，得到 %q", got.DeleteFingerprint)
	}
	if got.DeletePaths != nil {
		t.Fatalf("清空后 DeletePaths 必须为 nil，得到 %#v", got.DeletePaths)
	}
}

// FIX 2（后端）：删除闸门结果必须区分"数量阈值挂起（可确认）"与"硬拒绝（不可确认）"。
// 只有 NeedsConfirm 臂写 DeleteNeedsConfirm=true；任何硬拒绝都必须把它清回 false。
func TestMaybeDeleteRecordsNeedsConfirmOnlyForThreshold(t *testing.T) {
	seed := func(t *testing.T, r *ruleRuntime, n int) {
		t.Helper()
		local := r.rule.LocalPath
		rels := make([]string, 0, n)
		r.state.With(func(d *StateFile) {
			for i := 0; i < n; i++ {
				rel := fmt.Sprintf("d%02d.txt", i)
				if err := os.WriteFile(filepath.Join(local, rel), []byte("abc"), 0644); err != nil {
					t.Fatal(err)
				}
				st, err := osStat(filepath.Join(local, rel))
				if err != nil {
					t.Fatal(err)
				}
				d.Entries[rel] = &Entry{RemoteSize: st.Size, RemoteMTime: "t",
					LocalSize: st.Size, LocalMTime: st.ModTime, HasLocal: true}
				rels = append(rels, rel)
			}
		})
		r.mu.Lock()
		r.pendingDel = append([]string{}, rels...)
		r.refreshDeleteFingerprint()
		r.mu.Unlock()
	}

	t.Run("阈值臂：挂起并标记可确认", func(t *testing.T) {
		lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
		c, rule, _ := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
		r := attachRuntime(t, c, rule)
		seed(t, r, 10)
		r.mu.Lock()
		r.firstRound = false
		r.blockDels = false
		r.mu.Unlock()
		// curCount=100：CountKnown=true 且 CurCount!=0（不触发"远端疑似清空"硬拒绝），
		// 10*2 < PrevCount(110)，因此只可能命中绝对阈值 n>=10 的 NeedsConfirm 臂。
		c.maybeDelete(r, 100)
		got := c.Stats()[rule.ID]
		if !got.DeleteNeedsConfirm {
			t.Fatalf("阈值臂挂起必须标记 DeleteNeedsConfirm=true，得到 %+v", got)
		}
		if got.DeletePending != 10 || got.DeleteFingerprint == "" {
			t.Fatalf("阈值臂必须保留挂起清单与指纹，得到 %+v", got)
		}
	})

	t.Run("硬拒绝臂：清回不可确认", func(t *testing.T) {
		cases := map[string]func(r *ruleRuntime){
			"镜像删除未开启": func(r *ruleRuntime) { r.rule.MirrorDelete = false },
			"根目录消失":   func(r *ruleRuntime) { r.blockDels = true },
			"第一轮":     func(r *ruleRuntime) { r.firstRound = true },
		}
		for name, prep := range cases {
			t.Run(name, func(t *testing.T) {
				lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
				c, rule, _ := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
				r := attachRuntime(t, c, rule)
				seed(t, r, 10)
				r.mu.Lock()
				r.firstRound = false
				r.blockDels = false
				prep(r)
				// 假装上一轮闸门曾以阈值挂起：硬拒绝必须主动把它清回 false。
				r.stats.DeleteNeedsConfirm = true
				r.mu.Unlock()
				c.maybeDelete(r, 100)
				if got := c.Stats()[rule.ID]; got.DeleteNeedsConfirm {
					t.Fatalf("硬拒绝臂 %q 绝不能标记可确认，得到 %+v", name, got)
				}
			})
		}
	})

	t.Run("远端疑似清空：硬拒绝", func(t *testing.T) {
		lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
		c, rule, _ := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
		r := attachRuntime(t, c, rule)
		seed(t, r, 1)
		r.mu.Lock()
		r.firstRound = false
		r.blockDels = false
		r.stats.DeleteNeedsConfirm = true
		r.mu.Unlock()
		// curCount=0 且 PrevCount=1>0 ⇒ "远端本轮为空而上一轮非空"硬拒绝。
		c.maybeDelete(r, 0)
		if got := c.Stats()[rule.ID]; got.DeleteNeedsConfirm {
			t.Fatalf("远端疑似清空臂绝不能标记可确认，得到 %+v", got)
		}
	})
}

// FIX 2b（纵深防御）：闸门未以阈值臂挂起（DeleteNeedsConfirm=false）时，即使指纹
// 看似匹配，ConfirmDeletes 也必须拒绝，绝不能凭一个硬拒绝过的批次执行删除。
func TestConfirmDeletesRefusesWhenGateDidNotSuspend(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	c, rule, local := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)

	target := filepath.Join(local, "gone.txt")
	if err := os.WriteFile(target, []byte("doomed"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := osStat(target)
	if err != nil {
		t.Fatal(err)
	}
	// 基线取当前 stat ⇒ 若被错误放行，"本地已改"护栏不会救它，会真的被删。
	r.state.With(func(d *StateFile) {
		d.Entries["gone.txt"] = &Entry{RemoteSize: st.Size, RemoteMTime: "t",
			LocalSize: st.Size, LocalMTime: st.ModTime, HasLocal: true}
	})
	r.mu.Lock()
	r.pendingDel = []string{"gone.txt"}
	r.refreshDeleteFingerprint() // 会重置 DeleteNeedsConfirm=false（模拟硬拒绝臂）
	fp := r.delFP
	r.mu.Unlock()

	if err := c.ConfirmDeletes(rule.ID, fp); err == nil {
		t.Fatal("闸门未以阈值臂挂起时 ConfirmDeletes 必须拒绝")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("被拒绝的确认绝不能执行删除: %v", err)
	}
}

// FIX 2b 正向护栏：真的由阈值臂挂起的批次仍可正常确认并执行，且执行后清回不可确认。
func TestConfirmDeletesStillWorksWhenGateSuspended(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	c, rule, local := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)

	target := filepath.Join(local, "gone.txt")
	if err := os.WriteFile(target, []byte("doomed"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := osStat(target)
	if err != nil {
		t.Fatal(err)
	}
	r.state.With(func(d *StateFile) {
		d.Entries["gone.txt"] = &Entry{RemoteSize: st.Size, RemoteMTime: "t",
			LocalSize: st.Size, LocalMTime: st.ModTime, HasLocal: true}
	})
	r.mu.Lock()
	r.pendingDel = []string{"gone.txt"}
	r.refreshDeleteFingerprint()
	r.stats.DeleteNeedsConfirm = true // 模拟 maybeDelete 的阈值臂结论
	fp := r.delFP
	r.mu.Unlock()

	if err := c.ConfirmDeletes(rule.ID, fp); err != nil {
		t.Fatalf("阈值臂挂起的批次必须可确认: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("确认后文件应被删除，stat err=%v", err)
	}
	if got := c.Stats()[rule.ID]; got.DeleteNeedsConfirm {
		t.Fatalf("执行后必须清回 DeleteNeedsConfirm=false，得到 %+v", got)
	}
}

// FIX 2 residual（a）：远端根消失/队列溢出时，consume 立即把"可确认"标记作废。
// 本测试故意让 ListMany 为 nil，使随后的 align 直接返回（不重算清单、不清标记），
// 从而隔离出 consume 分支自身的清标记行为。
func TestConsumeUntrustedRootClearsDeleteConfirm(t *testing.T) {
	c, rule, _ := newManualCtrl(t, nil, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)

	r.mu.Lock()
	r.pendingDel = []string{"gone.txt"}
	r.refreshDeleteFingerprint()
	r.stats.DeleteNeedsConfirm = true // 模拟 maybeDelete 阈值臂留下的可确认状态
	fp := r.delFP
	r.mu.Unlock()
	if !c.Stats()[rule.ID].DeleteNeedsConfirm {
		t.Fatal("前置：应处于可确认状态")
	}

	// 直接把 RootGone 事件送进 consume：分支内应清标记；align 因 ListMany==nil 直接返回。
	ch := make(chan watch.Event, 1)
	ch <- watch.Event{Kind: watch.KindRootGone}
	close(ch)
	if dropped := c.consume(r, ch); !dropped {
		t.Fatal("通道关闭时 consume 应返回 true（断开/需要重连）")
	}

	if got := c.Stats()[rule.ID]; got.DeleteNeedsConfirm {
		t.Fatalf("根目录消失后必须作废可确认标记，得到 %+v", got)
	}
	if err := c.ConfirmDeletes(rule.ID, fp); err == nil {
		t.Fatal("根目录消失后即使指纹未变，ConfirmDeletes 也必须拒绝")
	}
}

// FIX 2 residual（b）：对账扫描出错/不完整时 align 提前返回，必须同时作废"可确认"标记。
// 否则陈旧标记 + 陈旧指纹会让 ConfirmDeletes 在 fetchMeta 全部"未知"的情况下照删整批。
func TestAlignIncompleteScanClearsDeleteConfirm(t *testing.T) {
	// 空 tree：ScanTree 对根路径返回"未知"（map 缺 key）-> snap.Complete=false。
	lm := &scriptedListMany{tree: map[string][]sftp.Item{}}
	c, rule, local := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)

	target := filepath.Join(local, "gone.txt")
	if err := os.WriteFile(target, []byte("doomed"), 0644); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.pendingDel = []string{"gone.txt"}
	r.refreshDeleteFingerprint()
	r.stats.DeleteNeedsConfirm = true // 模拟 maybeDelete 阈值臂留下的可确认状态
	fp := r.delFP
	r.mu.Unlock()

	c.align(r) // 扫描不完整 -> 提前返回

	got := c.Stats()[rule.ID]
	if got.DeleteNeedsConfirm {
		t.Fatalf("扫描不完整后必须作废可确认标记，得到 %+v", got)
	}
	// 按设计：pendingDel/指纹不动，交给下一轮完整对账重算并重跑闸门。
	if got.DeleteFingerprint != fp || got.DeletePending != 1 {
		t.Fatalf("不得改动挂起清单/指纹，得到 %+v（期望 fp=%q pending=1）", got, fp)
	}
	if err := c.ConfirmDeletes(rule.ID, fp); err == nil {
		t.Fatal("扫描不完整后即使指纹未变，ConfirmDeletes 也必须拒绝")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("被拒绝的确认绝不能删本地文件: %v", err)
	}
}
