// Package localfs 提供本地文件系统的纯本地操作（复制/搜索），只依赖标准库，
// 不依赖 internal/sftp 或 internal/watch，便于单测与复用。
package localfs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Copy 复制文件或目录树到 dst；调用方负责按冲突策略决定覆盖/改名/跳过。
// 覆盖已存在文件时直接截断重写（与"覆盖全部"策略一致）。
func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	// 符号链接一律跳过：不跟随（否则指向目录的链接会让整次复制以 EISDIR 半途失败），
	// 也不复制链接本身（本工具不承诺保留链接语义）。
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := Copy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// IsSubPath 判断 child 是否位于 parent 之内（含相等）。用于拒绝"复制/移动到自身子树"。
func IsSubPath(parent, child string) bool {
	p := strings.TrimRight(filepath.Clean(parent), string(filepath.Separator))
	c := filepath.Clean(child)
	if p == c {
		return true
	}
	return strings.HasPrefix(c, p+string(filepath.Separator))
}
