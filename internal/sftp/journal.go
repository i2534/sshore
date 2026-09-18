package sftp

// swapJournal 是 backup-swap 崩溃恢复日志（Task 8 实现；spec D8/决策 backup-swap）。
//
// 这里只做**前向声明**：Go 没有部分结构体，GoBackend 必须一次性声明全部字段
// （技术审核 S5），因此 Task 6 先把类型骨架落下，Task 8 只在本文件补字段与方法，
// 不再改 GoBackend 的字段列表。
type swapJournal struct{}
