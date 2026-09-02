package forward

import (
	"fmt"

	"sshore/internal/config"
)

// ValidateTunnel 校验规则字段(创建/编辑/导入共用)：
//   - host 必须是安全别名(复用 ValidateHost)
//   - listen_port 必须在 1-65535
//   - local/remote 模式要求 target_host 非空、target_port 在 1-65535
//     (空目标主机是历史 bug 的根源:`-L 23080::3080` 会让 ssh 解析空主机名,
//     表现为连接即 RST,而进程不退出、状态停在 connected)
//   - dynamic 模式不要求目标字段
//
// 返回 nil 或第一个校验错误。
func ValidateTunnel(t config.Tunnel) error {
	if !ValidateHost(t.Host) {
		return fmt.Errorf("invalid host alias %q", t.Host)
	}
	if t.ListenPort <= 0 || t.ListenPort > 65535 {
		return fmt.Errorf("listen port must be 1-65535, got %d", t.ListenPort)
	}
	switch t.Mode {
	case "local", "remote":
		if t.TargetHost == "" {
			return fmt.Errorf("target host is required for %s tunnels", t.Mode)
		}
		if t.TargetPort <= 0 || t.TargetPort > 65535 {
			return fmt.Errorf("target port must be 1-65535, got %d", t.TargetPort)
		}
	case "dynamic":
		// 无目标字段要求
	default:
		return fmt.Errorf("invalid mode %q", t.Mode)
	}
	return nil
}
