package sync

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// SafeRelPath 校验来自远端的相对路径并归一化。
// 远端能回传任意文件名，这里必须拒绝：空路径、"." / ".."、绝对路径、
// 含 NUL 或其它控制字符的路径（控制字符会把 sftp 批处理劈成两条命令，
// 而批处理里以 '!' 开头的行会执行本地 shell 命令）。
func SafeRelPath(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", false
	}
	for _, r := range rel {
		if r == 0 || unicode.IsControl(r) {
			return "", false
		}
	}
	// Windows 上反斜杠也是分隔符，先统一成正斜杠再判层级。
	norm := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(norm, "/") || strings.Contains(norm, ":") {
		return "", false
	}
	clean := path.Clean(norm)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if clean != norm && clean+"/" != norm { // path.Clean 归一后允许 "d/./c.txt" -> "d/c.txt"
		clean = path.Clean(norm)
	}
	return clean, true
}

// LocalTarget 把相对路径映射到本地绝对路径，并用 filepath.Rel 二次确认
// 结果**仍在 root 之内**——这是路径穿越的最后一道防线，不是可选优化。
func LocalTarget(root, rel string) (string, error) {
	safe, ok := SafeRelPath(rel)
	if !ok {
		return "", fmt.Errorf("非法相对路径: %q", rel)
	}
	target := filepath.Join(root, filepath.FromSlash(safe))
	back, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目标逃出根目录: %q", rel)
	}
	return target, nil
}
