package sync

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// SafeRelPath 校验来自远端的相对路径并归一化。远端能回传任意文件名，
// 这里的每一项都是**拒绝**，不是清洗：
//   - 空路径、"." / ".."，以及任何 dot-dot 逃逸（a/../../b、a/b/../../..）；
//   - 绝对路径（前导 "/"）；
//   - 含 NUL 或其它控制字符的路径（控制字符会把 sftp 批处理劈成两条命令，
//     而批处理里以 '!' 开头的行会执行本地 shell 命令）；
//   - 首尾空白（空格与 NBSP 等）：裁剪会让 "a " 与 "a" 成为别名，远端路径与
//     本地目标名也会不一致，所以一律拒绝而不 TrimSpace。
//
// 另外做两件归一化/拒绝：
//   - 反斜杠统一成正斜杠（Windows 远端可能回传带反斜杠的路径）；
//   - 含 ":" 的路径一律拒绝：本地 root 可能在 Windows 上，":" 在那里意味着
//     盘符（"C:foo" 是驱动器相对路径，filepath.Join/Rel 会按 volume 处理）或
//     NTFS 备用数据流（"file:stream"）。一条平台无关规则比逐平台探测更简单、
//     更可预测，代价是放弃合法 Unix 名（如 "a:b.txt"）。
//
// 其余情况返回 path.Clean 归一后的结果（"d/./c.txt" → "d/c.txt"、"a//b" → "a/b"）。
func SafeRelPath(rel string) (string, bool) {
	// 控制字符必须在任何裁剪**之前**、对原始输入检查：若先 TrimSpace，
	// 首尾的 TAB/LF/CR 会被静默吃掉，"a\r" 与 "a" 就成了别名。契约是拒绝。
	if rel == "" {
		return "", false
	}
	for _, r := range rel {
		if r == 0 || unicode.IsControl(r) {
			return "", false
		}
	}
	if strings.TrimSpace(rel) != rel {
		return "", false
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
