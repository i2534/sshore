package sync

import (
	"testing"

	"sshore/internal/watch"
)

func TestDecideTable(t *testing.T) {
	const (
		rs, rm = int64(10), "2026-09-10T10:00:00Z"
		ls, lm = int64(10), "2026-09-10T10:00:01Z"
	)
	full := &Entry{RemoteSize: rs, RemoteMTime: rm, LocalSize: ls, LocalMTime: lm, HasLocal: true}
	cases := []struct {
		name   string
		kind   watch.Kind
		ent    *Entry
		local  LocalState
		mirror bool
		want   Action
	}{
		{"无基线+本地不存在 → GET", watch.KindCreate, nil, LocalState{}, false, ActionGet},
		{"无基线+本地同大小 → 采纳", watch.KindCreate, &Entry{RemoteSize: rs}, LocalState{Exists: true, Size: rs}, false, ActionAdopt},
		{"无基线+本地大小不同 → 冲突", watch.KindCreate, &Entry{RemoteSize: rs}, LocalState{Exists: true, Size: 99}, false, ActionConflict},
		{"有基线+与基线一致 → GET", watch.KindWrite, full, LocalState{Exists: true, Size: ls, ModTime: lm}, false, ActionGet},
		{"有基线+本地被改 → 冲突", watch.KindWrite, full, LocalState{Exists: true, Size: 77, ModTime: "x"}, false, ActionConflict},
		{"有基线+本地被删 → 重新 GET", watch.KindWrite, full, LocalState{Exists: false}, false, ActionGet},
		{"删除+与基线一致+镜像关 → 跳过", watch.KindDelete, full, LocalState{Exists: true, Size: ls, ModTime: lm}, false, ActionSkip},
		{"删除+与基线一致+镜像开 → 删除", watch.KindDelete, full, LocalState{Exists: true, Size: ls, ModTime: lm}, true, ActionDelete},
		{"删除+本地被改 → 跳过", watch.KindDelete, full, LocalState{Exists: true, Size: 77}, true, ActionSkip},
		{"删除+无基线 → 跳过", watch.KindDelete, nil, LocalState{Exists: true}, true, ActionSkip},
		{"删除+本地已不存在 → 跳过", watch.KindDelete, full, LocalState{Exists: false}, true, ActionSkip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := Decide(c.kind, c.ent, c.local, c.mirror)
			if got != c.want {
				t.Fatalf("got %v (%s) want %v", got, reason, c.want)
			}
			if reason == "" {
				t.Fatal("每个决策都必须带可读原因，用于日志")
			}
		})
	}
}
