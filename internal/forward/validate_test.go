package forward

import (
	"strings"
	"testing"

	"sshore/internal/config"
)

func TestValidateTunnelLocalOK(t *testing.T) {
	tr := config.Tunnel{ID: "x", Host: "ai", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 23080,
		TargetHost: "127.0.0.1", TargetPort: 3080}
	if err := ValidateTunnel(tr); err != nil {
		t.Fatalf("valid local tunnel rejected: %v", err)
	}
}

// 空目标主机必须拒绝——这是历史坏规则(`-L 23080::3080`)的根源:
// ssh 解析空主机名失败 → 连接即 RST,而进程永不退出 → 界面误报 connected。
func TestValidateTunnelRejectsEmptyTargetHost(t *testing.T) {
	tr := config.Tunnel{ID: "x", Name: "DSH", Host: "ai", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 23080, TargetPort: 3080}
	err := ValidateTunnel(tr)
	if err == nil {
		t.Fatal("empty target host must be rejected")
	}
	if !strings.Contains(err.Error(), "target host") {
		t.Fatalf("error should mention target host: %v", err)
	}
	// remote 模式同样拒绝
	tr.Mode = "remote"
	if err := ValidateTunnel(tr); err == nil {
		t.Fatal("empty target host must be rejected for remote too")
	}
}

func TestValidateTunnelDynamicNoTarget(t *testing.T) {
	tr := config.Tunnel{ID: "x", Host: "ai", Mode: "dynamic",
		ListenBind: "127.0.0.1", ListenPort: 1080}
	if err := ValidateTunnel(tr); err != nil {
		t.Fatalf("dynamic tunnel rejected: %v", err)
	}
}

func TestValidateTunnelBadPorts(t *testing.T) {
	base := config.Tunnel{ID: "x", Host: "ai", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 0,
		TargetHost: "127.0.0.1", TargetPort: 3080}
	if err := ValidateTunnel(base); err == nil {
		t.Fatal("listen_port 0 must be rejected")
	}
	base.ListenPort = 23080
	base.TargetPort = 0
	if err := ValidateTunnel(base); err == nil {
		t.Fatal("target_port 0 must be rejected")
	}
	base.TargetPort = 70000
	if err := ValidateTunnel(base); err == nil {
		t.Fatal("target_port 70000 must be rejected")
	}
}

func TestValidateTunnelInvalidMode(t *testing.T) {
	tr := config.Tunnel{ID: "x", Host: "ai", Mode: "bogus",
		ListenBind: "127.0.0.1", ListenPort: 23080}
	if err := ValidateTunnel(tr); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}
