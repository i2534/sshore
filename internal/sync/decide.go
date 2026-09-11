package sync

import "sshore/internal/watch"

// Entry 是一个已同步文件的记录。
//
// **字段级写入规则（违反会静默丢失用户数据）**：
//   - RemoteSize/RemoteMTime **只能由 ls 结果写入**（扫描或决策前补齐）；
//   - LocalSize/LocalMTime 只在两种情况写入：一次成功 rename 之后，或首轮采纳；
//   - HasLocal 是"我们建立了本地基线"的判定依据，比 WrittenAt 更可靠。
//
// CONFLICT 的文件**不写入 Entries**——否则下次远端再改它就会走进
// "有基线且一致 ⇒ 远端赢"而覆盖用户的修改，绕过整套冲突保护。
type Entry struct {
	RemoteSize  int64  `json:"remote_size"`
	RemoteMTime string `json:"remote_mtime"`
	LocalSize   int64  `json:"local_size"`
	LocalMTime  string `json:"local_mtime"`
	WrittenAt   string `json:"written_at,omitempty"`
	Adopted     bool   `json:"adopted,omitempty"`
	HasLocal    bool   `json:"has_local"`
}

func (e *Entry) HasBaseline() bool { return e != nil && e.HasLocal }

// LocalState 是本地磁盘现状（由调用方 stat 得到）。
type LocalState struct {
	Exists  bool
	Size    int64
	ModTime string
}

type Action int

const (
	ActionSkip Action = iota
	ActionGet
	ActionAdopt
	ActionConflict
	ActionDelete
	// ActionSaveAs：远端版本另存为 <name>.remote-<ts>，**本地原文件保留**。
	// 必须与 ActionGet 分开：否则"另存为"会直接覆盖本地文件（数据丢失）。
	ActionSaveAs
)

func (a Action) String() string {
	switch a {
	case ActionGet:
		return "get"
	case ActionAdopt:
		return "adopt"
	case ActionConflict:
		return "conflict"
	case ActionDelete:
		return "delete"
	case ActionSaveAs:
		return "save_as"
	default:
		return "skip"
	}
}

// Decide 实现决策表。它永远是纯函数：不做 IO、不改状态，便于逐行测试。
//
// 贯穿全表的原则是"未知不等于不存在"：远端元信息缺失时，本地已有的同名文件
// 必须走冲突（交用户裁决）而不是直接覆盖。
//
// kind 必须是**文件级**事件：watch.KindCreate / watch.KindWrite / watch.KindDelete。
// watch.KindDirAdded / KindDirGone / KindOverflow / KindRootGone 必须先由调用方
// 展开成文件级事件再传入（每个文件一次调用）；把目录或溢出事件直接喂进来会落入
// "远端内容变更"分支，语义不成立。
func Decide(kind watch.Kind, ent *Entry, local LocalState, mirrorDelete bool) (Action, string) {
	if kind == watch.KindDelete {
		if !ent.HasBaseline() {
			return ActionSkip, "远端已删除，但本地没有对应基线，不动本地"
		}
		if !local.Exists {
			return ActionSkip, "远端已删除，本地也已不存在"
		}
		if local.Size != ent.LocalSize || local.ModTime != ent.LocalMTime {
			return ActionSkip, "远端已删除但本地被修改过，保留本地"
		}
		if !mirrorDelete {
			return ActionSkip, "远端已删除；镜像删除未开启，保留本地"
		}
		return ActionDelete, "远端已删除且本地未被改动，按镜像删除"
	}

	// ent 为 nil 表示远端元信息未知（ls 没补齐）。**"未知"不等于"远端不存在"**：
	// 本地已有同名文件时直接下载会无冲突提示地覆盖它；只有本地也确实不存在
	// （没有可丢的东西）时才保守下载。
	if ent == nil {
		if local.Exists {
			return ActionConflict, "远端元信息未知但本地已存在同名文件，不覆盖"
		}
		return ActionGet, "无远端元信息且本地不存在，保守下载"
	}
	if !ent.HasBaseline() {
		if !local.Exists {
			return ActionGet, "远端新增/变更，本地不存在"
		}
		if local.Size == ent.RemoteSize {
			return ActionAdopt, "本地已存在且大小一致，登记为已同步（不下载、未校验内容）"
		}
		return ActionConflict, "本地已存在同名文件且大小不同，不覆盖"
	}

	if !local.Exists {
		return ActionGet, "本地文件已被删除，重新下载"
	}
	if local.Size == ent.LocalSize && local.ModTime == ent.LocalMTime {
		return ActionGet, "远端变更，本地未被改动"
	}
	return ActionConflict, "远端变更但本地被修改过，不覆盖"
}
