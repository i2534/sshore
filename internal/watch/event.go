// Package watch 把远端变更探测的两条路径（inotify 常驻、SFTP 轮询）归一成
// 同一份事件契约。引擎层永远不知道事件来自哪条路径。
package watch

import (
	"context"
	"time"
)

type Kind string

const (
	KindCreate   Kind = "create"    // 新文件出现
	KindWrite    Kind = "write"     // 已有文件被修改
	KindDelete   Kind = "delete"    // 文件消失
	KindDirAdded Kind = "dir_added" // 目录进入监控树（含 mv 进来）→ 触发子树对账
	KindDirGone  Kind = "dir_gone"  // 目录离开监控树 → 触发子树对账
	KindOverflow Kind = "overflow"  // 内核队列溢出 → 强制全量对账
	KindRootGone Kind = "root_gone" // 根目录 UNMOUNT / DELETE_SELF / IGNORED
)

// Event 只携带"路径 + 意图"。**不带 Size/ModTime**：两条探测路径的元信息必须
// 同源，唯一同源来源是 ls 结果（见 ScanTree）。
type Event struct {
	RelPath string // 相对 root，"/" 分隔；空串表示根目录自身
	Kind    Kind
}

type Info struct {
	Mode     string // "inotify" | "poll"
	Reason   string // Mode=="poll" 时必填且必须具体
	Interval time.Duration
}

// Source 是探测层对上层暴露的唯一接口。
type Source interface {
	Start(ctx context.Context) (<-chan Event, error)
	Info() Info
	// Close 幂等，且**必须关闭 Start 返回的 channel**，否则引擎的 range 永不退出。
	Close() error
}
