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
type TransferError struct {
	Op        string
	Host      string
	Path      string
	RemoteMsg string
	Err       error
}

func (e *TransferError) Error() string {
	if e.RemoteMsg != "" {
		return fmt.Sprintf("%s %s: %s", e.Op, e.Path, e.RemoteMsg)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err)
	}
	return fmt.Sprintf("%s %s 失败", e.Op, e.Path)
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
}

// 编译期断言（Task 5 评审 M2）：两个后端必须始终满足新传输面 Backend 接口。
// 后续 task 若改了接口或任一后端的方法签名，这里第一时间编译失败，而不是等到运行期。
var (
	_ Backend = (*BatchBackend)(nil)
	_ Backend = (*GoBackend)(nil)
)
