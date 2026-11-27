// Package localfs 提供本地文件系统的纯本地操作（复制/搜索），只依赖标准库，
// 不依赖 internal/sftp 或 internal/watch，便于单测与复用。
package localfs

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
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

// closeFileOut 是 copyFile 显式 Close 的注入点（**仅测试注入**，与
// internal/sftp 的 scanCheckpoint 同风格）：生产恒走真实 Close。
var closeFileOut = func(f *os.File) error { return f.Close() }

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
	// 兜底防泄漏：提前 return 的路径也会关闭；显式 Close 之后的第二次 Close
	// 返回 ErrClosed，在这里被忽略（double-close 安全）。
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	// A5: 显式 Close 并合并错误 —— 写满盘/NFS 写回失败恰恰发生在 Close 时刻，
	// 忽略它就是假成功。两者都失败时优先返回 Copy 错误（首因）。
	cerr := closeFileOut(out)
	if err != nil {
		return err
	}
	return cerr
}

// IsSubPath 判断 child 是否位于 parent 之内（含相等）。用于拒绝"复制/移动到自身子树"。
// A6: Windows 路径大小写不敏感（C:\Data 与 c:\data\sub 必须判 true），否则自身
// 子树守卫可被大小写变体绕过；POSIX 保持大小写敏感语义。
func IsSubPath(parent, child string) bool {
	return isSubPathGOOS(parent, child, runtime.GOOS)
}

// isSubPathGOOS 是 goos 参数化实现，供测试在 Linux 上直调验证 Windows 语义
// （与 internal/update/extract.go 的 goos 参数风格一致）。
func isSubPathGOOS(parent, child, goos string) bool {
	if goos == "windows" {
		// Windows：大小写不敏感、两种分隔符等价。Linux 上 filepath.Clean 不认
		// 反斜杠，故统一成斜杠后 path.Clean（保留 UNC 开头双斜杠），再折叠大小写。
		norm := func(s string) string {
			s = strings.ReplaceAll(s, "\\", "/")
			unc := strings.HasPrefix(s, "//")
			s = path.Clean(s)
			if unc && !strings.HasPrefix(s, "//") {
				s = "/" + s
			}
			return strings.ToLower(s)
		}
		p := strings.TrimRight(norm(parent), "/")
		c := norm(child)
		if p == c {
			return true
		}
		return strings.HasPrefix(c, p+"/")
	}
	p := strings.TrimRight(filepath.Clean(parent), string(filepath.Separator))
	c := filepath.Clean(child)
	if p == c {
		return true
	}
	return strings.HasPrefix(c, p+string(filepath.Separator))
}
