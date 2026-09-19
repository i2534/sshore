//go:build !windows

package update

import "golang.org/x/sys/unix"

// FreeSpace 返回 dir 所在文件系统的可用字节数（Linux/BSD 用 Statfs）。
func FreeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
