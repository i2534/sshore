package sync

import (
	"fmt"
	"strings"
	"unicode"

	"sshore/internal/config"
	"sshore/internal/forward"
)

// ValidateSyncRule 在创建/编辑时即拒绝坏规则（对齐 forward.ValidateTunnel 的做法），
// 而不是拖到启动时才报错。
//
// 注意 host 的校验复用 forward.ValidateHost，其正则实际是
// ^[A-Za-z0-9][A-Za-z0-9._-]*$（不只是"禁止前导 -"）。
func ValidateSyncRule(r config.SyncRule) error {
	if !forward.ValidateHost(r.Host) {
		return fmt.Errorf("主机名不合法: %q", r.Host)
	}
	if r.Kind != "dir" && r.Kind != "file" {
		return fmt.Errorf("kind 必须是 dir 或 file，得到 %q", r.Kind)
	}
	if strings.TrimSpace(r.RemotePath) == "" {
		return fmt.Errorf("远端路径不能为空")
	}
	for _, ru := range r.RemotePath {
		if unicode.IsControl(ru) {
			return fmt.Errorf("远端路径含控制字符")
		}
	}
	if !strings.HasPrefix(r.RemotePath, "/") && !strings.HasPrefix(r.RemotePath, "~") {
		return fmt.Errorf("远端路径必须是绝对路径或以 ~ 开头")
	}
	if strings.TrimSpace(r.LocalPath) == "" {
		return fmt.Errorf("本地路径不能为空")
	}
	if r.MaxDepth < -1 || r.MaxDepth > 64 {
		return fmt.Errorf("max_depth 必须在 -1 或 [0,64] 之间，得到 %d", r.MaxDepth)
	}
	if r.PollIntervalS < 1 || r.PollIntervalS > 3600 {
		return fmt.Errorf("poll_interval_s 必须在 [1,3600] 之间，得到 %d", r.PollIntervalS)
	}
	if r.Kind == "file" && r.MirrorDelete {
		return fmt.Errorf("kind=file 时 mirror_delete 无意义（源消失一律不删本地）")
	}
	return nil
}
