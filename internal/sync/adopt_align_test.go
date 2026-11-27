package sync

import (
	"os"
	"path/filepath"
	"testing"

	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// Y1(a)：单文件规则首轮 Adopt（本地已存在同名同大小文件）后，条目必须登记
// 远端真实元信息。Adopt 分支若只写 Local*/HasLocal/Adopted 而漏写 Remote*，
// 条目 Remote* 恒为 0/""，与 Get 分支（ctrl.go 显式补写 Remote*）不一致。
// 断言用手工推导字面量：远端 Size=5、ModTime="2026-09-10 10:00"。
func TestFileAdoptRecordsRemoteMetadata(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)
	// 本地已存在同名同大小（5 字节）文件 ⇒ Decide 走 Adopt。
	if err := os.WriteFile(filepath.Join(local, "a.conf"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}

	c.align(r) // 首轮 alignFile → Adopt

	if xf.calls != 0 {
		t.Fatalf("Adopt 不得下载，calls=%d", xf.calls)
	}
	var got Entry
	found := false
	r.state.With(func(d *StateFile) {
		if e := d.Entries["a.conf"]; e != nil {
			got = *e
			found = true
		}
	})
	if !found {
		t.Fatal("Adopt 后必须登记条目")
	}
	if got.RemoteSize != 5 || got.RemoteMTime != "2026-09-10 10:00" {
		t.Fatalf("Adopt 必须登记远端元信息（与 Get 分支对齐），得到 RemoteSize=%d RemoteMTime=%q",
			got.RemoteSize, got.RemoteMTime)
	}
	if !got.HasLocal || !got.Adopted || got.LocalSize != 5 {
		t.Fatalf("Adopt 基线字段不正确：%+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.conf")); string(b) != "other" {
		t.Fatalf("Adopt 绝不能改写本地文件，content=%q", string(b))
	}
}

// Y1(b)：Adopt 漏写 Remote* 的实际危害——下一轮**对账**把 Remote* 由 0 比对成
// 真实值，判成"远端变更"而无谓重下载（本地若改过则假冲突）。
// 用事件路径触发 Adopt（entries 为空、fetchMeta 补齐真实元信息，不预登记 Remote*），
// 再跑一轮带远端 diff 的对账（c.align）。修复前第二轮应重下载；修复后保持安静。
func TestAdoptThenAlignDoesNotRedownload(t *testing.T) {
	local := t.TempDir()
	rule := dirRule(local)
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r":       {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
		"/r/a.txt": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	c, r := newEventCtrl(t, rule, lm.list, xf)

	// 事件路径 Adopt：本地同名同大小，远端元信息齐全。
	driveEvent(t, c, r, watch.Event{RelPath: "a.txt", Kind: watch.KindWrite})
	if xf.calls != 0 {
		t.Fatalf("Adopt 不得下载，calls=%d", xf.calls)
	}
	var got Entry
	r.state.With(func(d *StateFile) {
		if e := d.Entries["a.txt"]; e != nil {
			got = *e
		}
	})
	if got.RemoteSize != 5 || got.RemoteMTime != "2026-09-10 10:00" {
		t.Fatalf("Adopt 必须登记远端元信息，得到 RemoteSize=%d RemoteMTime=%q",
			got.RemoteSize, got.RemoteMTime)
	}

	// 下一轮对账：远端未变 ⇒ 不得重下载、不得产生冲突、不得覆盖本地。
	c.align(r)
	if xf.calls != 0 {
		t.Fatalf("Adopt 后对账不得重下载（Remote* 已登记），calls=%d", xf.calls)
	}
	if cs := c.Conflicts(rule.ID); len(cs) != 0 {
		t.Fatalf("Adopt 后对账不得产生假冲突，得到 %+v", cs)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(b) != "other" {
		t.Fatalf("本地文件不得被覆盖，content=%q", string(b))
	}
}

// Y2：AlignScanned 每轮对账必须从 0 重新累计，不得超过 AlignTotal。
// align 每轮把 AlignTotal 重置为本轮快照大小，但 AlignScanned 只 ++ 不清零，
// 第二轮起 AlignScanned > AlignTotal，前端 alignText 在 scanned>=total 时返回
// 空串 ⇒ 后续对账进度永不显示。预置与远端一致的基线（keepEntry），两轮都无
// changed，纯验计数器。快照 3 个文件 ⇒ 每轮 AlignTotal=3、AlignScanned 应=3。
func TestAlignScannedResetsEachRound(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {
			{Name: "a.txt", Size: 3, ModTime: "2026-09-10 10:00"},
			{Name: "b.txt", Size: 3, ModTime: "2026-09-10 10:00"},
			{Name: "c.txt", Size: 3, ModTime: "2026-09-10 10:00"},
		},
	}}
	c, rule, _ := newManualCtrl(t, lm.list, &fakeXfer{remote: map[string]string{}})
	r := attachRuntime(t, c, rule)
	r.state.With(func(d *StateFile) {
		d.Entries["a.txt"] = keepEntry()
		d.Entries["b.txt"] = keepEntry()
		d.Entries["c.txt"] = keepEntry()
	})

	c.align(r)
	s1 := c.Stats()[rule.ID]
	if s1.AlignTotal != 3 || s1.AlignScanned != 3 {
		t.Fatalf("第一轮：want AlignTotal=3 AlignScanned=3，得到 total=%d scanned=%d",
			s1.AlignTotal, s1.AlignScanned)
	}

	c.align(r)
	s2 := c.Stats()[rule.ID]
	if s2.AlignTotal != 3 {
		t.Fatalf("第二轮 AlignTotal 应重置为 3，得到 %d", s2.AlignTotal)
	}
	if s2.AlignScanned != 3 {
		t.Fatalf("第二轮 AlignScanned 应从 0 重新累计到 3，得到 %d（越界则前端进度永不显示）",
			s2.AlignScanned)
	}
	if s2.AlignScanned > s2.AlignTotal {
		t.Fatalf("AlignScanned(%d) 不得超过 AlignTotal(%d)", s2.AlignScanned, s2.AlignTotal)
	}
}

// Y3①：单文件远端未变 + 本地未改 ⇒ 第二轮对账必须零传输、本地内容不变。
// alignFile 若无条件 applyOne，Decide 的"有基线+本地未改"分支恒 GET，会把
// 用户本地文件覆盖成远端内容（探针实证 "other"→"hello"）。与 dir align 对齐：
// 条目已存在且 Remote* 与本轮远端一致时跳过 applyOne。
func TestFileAlignSkipsWhenRemoteUnchanged(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)
	if err := os.WriteFile(filepath.Join(local, "a.conf"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}

	c.align(r) // 首轮：Adopt，建立基线（Remote*=5/"2026-09-10 10:00"）
	if xf.calls != 0 {
		t.Fatalf("首轮 Adopt 不得下载，calls=%d", xf.calls)
	}

	c.align(r) // 第二轮：远端未变 ⇒ 必须跳过，零传输
	if xf.calls != 0 {
		t.Fatalf("远端未变的第二轮对账不得重下载，calls=%d", xf.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.conf")); string(b) != "other" {
		t.Fatalf("远端未变时本地文件不得被覆盖，content=%q", string(b))
	}
}

// Y3②（护栏）：远端真的变了 ⇒ 仍须正常触发 GET 并落地新内容。修复不得把
// "远端变更"也一并跳过。首轮 Adopt 后把远端改成 11 字节新内容，第二轮应下载。
func TestFileAlignDownloadsWhenRemoteChanged(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)
	if err := os.WriteFile(filepath.Join(local, "a.conf"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	c.align(r) // 首轮 Adopt

	lm.set("/srv/conf/a.conf", sftp.Item{Name: "a.conf", Size: 11, ModTime: "2026-09-10 11:00"})
	xf.mu.Lock()
	xf.remote["/srv/conf/a.conf"] = "hello world"
	xf.mu.Unlock()

	c.align(r) // 第二轮：远端变了 ⇒ GET
	if xf.calls != 1 {
		t.Fatalf("远端变更必须触发一次下载，calls=%d", xf.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.conf")); string(b) != "hello world" {
		t.Fatalf("远端变更后本地应落地新内容，content=%q", string(b))
	}
}

// Y3③：本地改过 + 远端未变 ⇒ 第二轮对账不得覆盖本地、不得刷假冲突。
// 语义依据（与 dir align 一致）：远端未变 ⇒ 跳过 applyOne ⇒ 不进 Decide，
// 本地修改被保留（keep），无冲突。修复前无条件 applyOne 会走 Decide 的
// "有基线+本地被改"分支 ⇒ Conflict（远端根本没变，属假冲突）。
func TestFileAlignKeepsLocalModifiedWhenRemoteUnchanged(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/srv/conf/a.conf": {{Name: "a.conf", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/srv/conf/a.conf": "hello"}}
	c, rule, local := newFileCtrl(t, lm.list, xf)
	r := attachRuntime(t, c, rule)
	if err := os.WriteFile(filepath.Join(local, "a.conf"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	c.align(r) // 首轮 Adopt

	// 用户修改本地（大小改变）。
	if err := os.WriteFile(filepath.Join(local, "a.conf"), []byte("modified!!"), 0644); err != nil {
		t.Fatal(err)
	}

	c.align(r) // 第二轮：远端未变 ⇒ 跳过，保留本地修改
	if xf.calls != 0 {
		t.Fatalf("远端未变时不得下载覆盖本地修改，calls=%d", xf.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(local, "a.conf")); string(b) != "modified!!" {
		t.Fatalf("本地修改必须保留，content=%q", string(b))
	}
	if cs := c.Conflicts(rule.ID); len(cs) != 0 {
		t.Fatalf("远端未变时本地修改不得刷假冲突，得到 %+v", cs)
	}
}
