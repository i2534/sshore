package sync

import (
	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// NewSftpAdapter 让 sync 通过 sftp.Ctrl 完成传输与列举，同时保持
// internal/sync 不依赖 sftp 的内部实现。
//
// 返回类型必须是 watch.ListManyFunc（它定义在 watch 包），不能在这里另起一个
// 同名命名类型——Deps.ListMany 的字段类型是 watch.ListManyFunc，命名类型不同
// 会赋值失败。
func NewSftpAdapter(c *sftp.Ctrl) (Transferer, watch.ListManyFunc) {
	return sftpTransfer{c: c}, sftpListMany{c: c}.ListMany
}

type sftpTransfer struct{ c *sftp.Ctrl }

func (t sftpTransfer) Get(host, user, remote, local string) error {
	return t.c.Get(host, user, remote, local)
}

type sftpListMany struct{ c *sftp.Ctrl }

func (t sftpListMany) ListMany(host, user string, paths []string) (map[string][]sftp.Item, error) {
	return t.c.ListMany(host, user, paths)
}
