// Package namepattern 提供 SFTP 深搜使用的名称匹配语义，远端与本地共用一份实现。
// 只依赖标准库。
package namepattern

import (
	"errors"
	"path"
	"strings"
)

// Match 判定单个条目是否命中：
//   - pattern 为空 → 命中一切；
//   - pattern 不含 glob 元字符（* ? [）→ 不区分大小写的子串匹配（basename 与相对路径都试）；
//   - pattern 含 glob 元字符 → 双方先转小写再 path.Match，保证"不区分大小写"在两种模式下一致；
//   - 非法 glob（ErrBadPattern）→ 回退为子串匹配，不静默丢弃。
func Match(pattern, rel, base string) bool {
	if pattern == "" {
		return true
	}
	p := strings.ToLower(pattern)
	if !strings.ContainsAny(pattern, "*?[") {
		return strings.Contains(strings.ToLower(base), p) || strings.Contains(strings.ToLower(rel), p)
	}
	baseL, relL := strings.ToLower(base), strings.ToLower(rel)
	okBase, errBase := path.Match(p, baseL)
	if errBase == nil && okBase {
		return true
	}
	okRel, errRel := path.Match(p, relL)
	if errRel == nil && okRel {
		return true
	}
	// 非法 glob（如未闭合的 [abc）→ 回退为子串匹配，绝不静默丢弃（spec §5.1）。
	if errors.Is(errBase, path.ErrBadPattern) || errors.Is(errRel, path.ErrBadPattern) {
		return strings.Contains(baseL, p) || strings.Contains(relL, p)
	}
	return false
}
