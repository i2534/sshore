package clipboard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TempDirPrefix 是剪贴板临时目录的名字前缀。
//
// 每次「复制 → 文件」（远程）都在 os.TempDir() 下新建一个该前缀的目录，
// 供 CF_HDROP 引用的文件落地：剪贴板存的是路径引用，因此这些目录在写入
// 剪贴板之后**不能**立即删除（粘贴那一刻资源管理器才去读文件）。
const TempDirPrefix = "sshore-clip-"

// NewTempDir 在 os.TempDir() 下新建唯一目录并返回其路径。
// root 口径与 CleanStaleTempDirs 一致（同为 os.TempDir()）。
func NewTempDir() (string, error) {
	dir, err := os.MkdirTemp("", TempDirPrefix)
	if err != nil {
		return "", fmt.Errorf("创建剪贴板临时目录失败: %w", err)
	}
	return dir, nil
}

// CleanStaleTempDirs 删除 root 下名字以 TempDirPrefix 开头、且 ModTime 早于
// now-minAge 的目录，返回删除的目录数。now 由调用方注入以便测试。
//
// 只处理目录（同前缀的普通文件不动）；os.ReadDir 非递归，因此不会误删
// 目录内的下载内容。单个条目失败不中断其余条目，错误汇总后返回。
func CleanStaleTempDirs(root string, minAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := now.Add(-minAge)
	removed := 0
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), TempDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
