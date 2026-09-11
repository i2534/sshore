package sync

import "testing"

func TestUpsertConflictDeduplicates(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 1, LocalSize: 2})
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 3, LocalSize: 4})
	if len(d.Conflicts) != 1 {
		t.Fatalf("同一路径只能有一条冲突，得到 %d", len(d.Conflicts))
	}
	if d.Conflicts[0].RemoteSize != 3 {
		t.Fatalf("应保留最新观测值，得到 %#v", d.Conflicts[0])
	}
}

// keep_local：只对齐 local_*，**remote_* 保持本次冲突时观测到的远端值**。
// 若把 remote_* 写成本地值，下一轮 poll 会判为 write，再按"远端赢"覆盖用户
// 刚刚选择保留的文件 —— 这是本条设计的全部意义。
func TestResolveKeepLocalKeepsRemoteFields(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{"a.conf": {}}}
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 88, RemoteMTime: "remote-t"})
	local := LocalState{Exists: true, Size: 91, ModTime: "local-t"}
	req, err := ResolveConflict(d, "a.conf", ConflictKeepLocal, local, "2026-09-10T12:00:00Z")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Action != ActionSkip {
		t.Fatalf("keep_local 不需要传输，得到 %v", req.Action)
	}
	e := d.Entries["a.conf"]
	if e.RemoteSize != 88 || e.RemoteMTime != "remote-t" {
		t.Fatalf("remote_* 被污染了: %#v", e)
	}
	if !e.HasLocal || e.LocalSize != 91 || e.LocalMTime != "local-t" {
		t.Fatalf("local_* 应对齐当前磁盘状态: %#v", e)
	}
	if len(d.Conflicts) != 0 {
		t.Fatal("解决后冲突条目必须移除")
	}
}

// take_remote：只返回"要传输"的请求，由规则 goroutine 串行消费。
func TestResolveTakeRemoteReturnsRequest(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	UpsertConflict(d, Conflict{RelPath: "b.conf"})
	req, err := ResolveConflict(d, "b.conf", ConflictTakeRemote, LocalState{}, "t")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Action != ActionGet || req.RelPath != "b.conf" {
		t.Fatalf("take_remote 必须返回 GET 请求: %#v", req)
	}
}

func TestResolveUnknownConflictIsError(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	if _, err := ResolveConflict(d, "nope", ConflictKeepLocal, LocalState{}, "t"); err == nil {
		t.Fatal("未知路径必须报错")
	}
}

// 未知动作必须在改动状态之前就被拒绝：否则调用方拿到 error 的同时，
// 队列里的冲突已被 splice 掉（落盘后从磁盘消失）。
func TestResolveUnknownActionKeepsConflict(t *testing.T) {
	d := &StateFile{}
	UpsertConflict(d, Conflict{RelPath: "c.conf", RemoteSize: 7})
	if _, err := ResolveConflict(d, "c.conf", ConflictAction("bogus"), LocalState{}, "t"); err == nil {
		t.Fatal("未知冲突动作必须报错")
	}
	if len(d.Conflicts) != 1 {
		t.Fatalf("未知动作不得移除冲突，得到 %d 条", len(d.Conflicts))
	}
	if d.Conflicts[0].RelPath != "c.conf" {
		t.Fatalf("冲突应保持原样，得到 %#v", d.Conflicts[0])
	}
	if d.Entries != nil {
		t.Fatalf("未知动作不得改动 d.Entries，得到 %#v", d.Entries)
	}
}

// I2 后续耐久性：ent==nil 的冲突（RemoteSize=0/RemoteMTime=""）下选择 keep_local
// 时，绝不能把零值当成远端基线写进 entry——否则下一轮对账必判"远端变化"并按
// "远端赢"回下载，覆盖用户刚刚选择保留的文件。未知元信息时干脆不登记任何基线。
func TestResolveKeepLocalWithoutRemoteMetaLeavesNoBaseline(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{
		"a.conf": {LocalSize: 5, LocalMTime: "local-t", HasLocal: true},
	}}
	UpsertConflict(d, Conflict{RelPath: "a.conf", LocalSize: 5, LocalMTime: "local-t"}) // 未知远端元信息
	local := LocalState{Exists: true, Size: 5, ModTime: "local-t"}
	req, err := ResolveConflict(d, "a.conf", ConflictKeepLocal, local, "2026-09-10T12:00:00Z")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Action != ActionSkip || req.RelPath != "a.conf" {
		t.Fatalf("keep_local 仍必须返回 skip 请求: %#v", req)
	}
	if e := d.Entries["a.conf"]; e != nil {
		t.Fatalf("未知远端元信息时不得写入零值基线，得到 %#v", e)
	}
	if len(d.Conflicts) != 0 {
		t.Fatal("解决后冲突条目必须移除")
	}
}
