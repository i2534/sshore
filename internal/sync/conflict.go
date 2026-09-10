package sync

import "fmt"

// Conflict 的类型定义在 Task 9 的 state.go（状态文件要序列化它），本任务只加动作。

type ConflictAction string

const (
	ConflictKeepLocal  ConflictAction = "keep_local"
	ConflictTakeRemote ConflictAction = "take_remote"
	ConflictSaveAs     ConflictAction = "save_as"
)

// TransferOrDelete 是"交给规则 goroutine 串行执行"的请求。
type TransferOrDelete struct {
	RelPath string
	Action  Action
	Detail  string
}

// UpsertConflict 按路径去重：同一路径只保留最新一次观测。
func UpsertConflict(d *StateFile, c Conflict) {
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == c.RelPath {
			d.Conflicts[i] = c
			return
		}
	}
	d.Conflicts = append(d.Conflicts, c)
}

// ResolveConflict 应用用户的裁决，返回需要由规则 goroutine 执行的请求。
// **本函数不做任何 IO**：绑定线程持状态锁时下载会与引擎形成 ABBA 死锁。
func ResolveConflict(d *StateFile, rel string, action ConflictAction, local LocalState, now string) (TransferOrDelete, error) {
	idx := -1
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == rel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return TransferOrDelete{}, fmt.Errorf("冲突不存在: %s", rel)
	}
	c := d.Conflicts[idx]
	d.Conflicts = append(d.Conflicts[:idx], d.Conflicts[idx+1:]...)
	if d.Entries == nil {
		d.Entries = map[string]*Entry{}
	}

	switch action {
	case ConflictKeepLocal:
		e := d.Entries[rel]
		if e == nil {
			e = &Entry{}
			d.Entries[rel] = e
		}
		// remote_* 保持本次冲突时观测到的远端值，绝不用本地值覆盖。
		e.RemoteSize, e.RemoteMTime = c.RemoteSize, c.RemoteMTime
		// local_* 对齐当前磁盘状态，否则下次同步会反复告警同一个文件。
		e.LocalSize, e.LocalMTime, e.HasLocal = local.Size, local.ModTime, local.Exists
		if !local.Exists {
			delete(d.Entries, rel)
		}
		return TransferOrDelete{RelPath: rel, Action: ActionSkip, Detail: "保留本地"}, nil
	case ConflictTakeRemote:
		return TransferOrDelete{RelPath: rel, Action: ActionGet, Detail: "用远端覆盖"}, nil
	case ConflictSaveAs:
		return TransferOrDelete{RelPath: rel, Action: ActionSaveAs, Detail: "另存远端副本"}, nil
	default:
		return TransferOrDelete{}, fmt.Errorf("未知冲突动作: %s", action)
	}
}
