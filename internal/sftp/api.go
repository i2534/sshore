package sftp

import (
	"context"
	"fmt"
)

type Direction string

const (
	DirDownload Direction = "download"
	DirUpload   Direction = "upload"
)

type Phase string

const (
	PhaseScan     Phase = "scan"
	PhaseTransfer Phase = "transfer"
)

// Progress 是传输进度事件载荷（spec §6.1）。
type Progress struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	Direction  Direction `json:"direction"`
	Name       string    `json:"name"`
	PartPath   string    `json:"partPath"`
	Done       int64     `json:"done"`
	Total      int64     `json:"total"` // <0 表示未知
	FilesDone  int       `json:"filesDone"`
	FilesTotal int       `json:"filesTotal"` // <0 表示未知
	Phase      Phase     `json:"phase"`
}

// TransferRequest 是一次传输的输入（spec §6.1）。
type TransferRequest struct {
	ID       string
	Host     string
	User     string
	Remote   string
	Local    string
	Resume   bool
	PartPath string
	// ResumeOffset 是续传起点（下载=本地 .part 大小；上传=远端 .part 大小）。
	// 由 Task 11 的 decideResume 计算后填入，Task 7/8 用它 Seek。
	ResumeOffset int64
	Atomic       bool // true=新面：我方 .part + 提交；false=legacy：直写目标
}

// TransferError 统一错误形状，RemoteMsg 保留远端/ssh 原文（spec D12）。
// PartPath 是本次传输实际使用的 .part 路径（未用到时为空）：失败/取消后调用方
// 凭它定位「保留下来、可续传」的临时文件，不需要解析错误文案（Task 7 起填充）。
type TransferError struct {
	Op        string
	Host      string
	Path      string
	PartPath  string
	RemoteMsg string
	Err       error

	// 目录传输的失败/取消路径回填（Task 12 修复轮 1 / I4）：已提交的文件数与字节数，
	// 以及尚未完成的文件数。RemainingFiles < 0 表示枚举已被 D16 降级（分母未知）。
	// 单文件任务、成功路径以及「尚未开始逐文件搬运」的失败保持零值。
	CommittedFiles int
	CommittedBytes int64
	RemainingFiles int
	// TreeCounts 标记上面三个计数是否已由目录传输回填。单文件任务、成功路径的零值绝不能
	// 被误读成「已传 0 个、还剩 0 个」—— Error() 只在本字段为真时才拼接计数文案。
	TreeCounts bool
}

func (e *TransferError) Error() string {
	var base string
	switch {
	case e.RemoteMsg != "":
		base = fmt.Sprintf("%s %s: %s", e.Op, e.Path, e.RemoteMsg)
	case e.Err != nil:
		base = fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err)
	default:
		base = fmt.Sprintf("%s %s 失败", e.Op, e.Path)
	}
	if e.TreeCounts {
		// I4 生产消费点（Task 12 修复轮 2）：绑定层把 error 当纯字符串回传前端，Task 14
		// 只能从文案里取「已传 N、剩 M」。若只把计数留在结构体字段里，它们永远到不了 UI。
		// RemainingFiles<0 表示枚举已降级或扫描未完成（分母未知）。
		remaining := ""
		if e.RemainingFiles >= 0 {
			remaining = fmt.Sprintf("剩余 %d 个文件", e.RemainingFiles)
		} else {
			remaining = "剩余文件数未知"
		}
		base += fmt.Sprintf("（已传输 %d 个文件/%d 字节，%s）", e.CommittedFiles, e.CommittedBytes, remaining)
	}
	return base
}

func (e *TransferError) Unwrap() error { return e.Err }

// Backend 是新传输面的最小契约；legacy 四参面保留在门面 Ctrl 上（Go 无重载）。
type Backend interface {
	List(host, user, path string) ([]Item, error)
	ListMany(host, user string, paths []string) (map[string][]Item, error)
	Home(host, user string) (string, error)
	// 方法名带 Transfer 前缀：BatchBackend 必须同时保留 legacy 四参的 Get/Put（Go 无重载，
	// 若接口也叫 Get(req,report) 就会与 legacy 版冲突 —— 技术审核 S3 的实测结论）。
	TransferGet(req TransferRequest, report func(Progress)) error
	TransferGetTree(req TransferRequest, report func(Progress)) error
	TransferPut(req TransferRequest, report func(Progress)) error
	TransferPutTree(req TransferRequest, report func(Progress)) error
	Remove(host, user, path string) error
	RemoveRecursive(host, user, path string) error
	Mkdir(host, user, path string) error
	Rename(host, user, oldPath, newPath string) error
	// Connect/Search 是门面与 app.go 需要的能力（自审 S2 补）。
	Connect(host, user string) error
	Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int, onProgress func(scanned int)) (SearchOutcome, error)
	Connected(host string) bool
	Disconnect(host string) error
	CloseAll()

	// Cancel 关闭某次传输独占的会话（库无逐请求取消）。幂等：未知/已完成 id 返回 false。
	Cancel(id string) bool
	// AtomicCapable 声明后端是否支持「.part + 提交」的原子传输。
	// 这是让绑定层**能力驱动**地填 TransferRequest.Atomic 的唯一依据：BatchBackend 返回
	// false（传 Atomic=true 会被 guardAtomic 硬拒，Task 6 的刻意裁决），GoBackend 返回 true。
	AtomicCapable() bool
}

// 编译期断言（Task 5 评审 M2）：两个后端必须始终满足新传输面 Backend 接口。
// 后续 task 若改了接口或任一后端的方法签名，这里第一时间编译失败，而不是等到运行期。
var (
	_ Backend = (*BatchBackend)(nil)
	_ Backend = (*GoBackend)(nil)
)
