package main

import (
	"path/filepath"
	"strings"
	"testing"

	"sshore/internal/config"
	"sshore/internal/forward"
)

// A2 回归：ImportCommand 必须逐条收集创建结果 —— 成功条保留在返回列表并落盘，
// 失败条（冲突/非法）不在返回列表、不落盘，且返回列出失败条目与原因的聚合错误。
// 修复前 `_ = a.CreateTunnel(t)` 吞错：失败条也出现在返回列表里、error 恒 nil。
func TestImportCommandPartialFailureKeepsSuccesses(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	// 预置占用 (prod, 127.0.0.1, 5432) 的规则：导入的第一条与之冲突必失败。
	if err := a.CreateTunnel(config.Tunnel{
		ID: "existing", Host: "prod", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432,
	}); err != nil {
		t.Fatalf("预置规则失败: %v", err)
	}

	ts, err := a.ImportCommand("ssh -N -L 5432:127.0.0.1:5432 -L 8080:127.0.0.1:80 prod")
	if err == nil {
		t.Fatal("存在冲突条目时 ImportCommand 必须返回聚合错误，绝不能吞错")
	}
	if !strings.Contains(err.Error(), "5432") {
		t.Fatalf("聚合错误必须列出失败条目（端口 5432），得到: %v", err)
	}
	if len(ts) != 1 {
		t.Fatalf("返回列表只应含成功创建的条目: want 1, got %d (%+v)", len(ts), ts)
	}
	if ts[0].ListenPort != 8080 {
		t.Fatalf("成功条目应是 8080 规则: %+v", ts[0])
	}
	if _, ok := a.findTunnel(ts[0].ID); !ok {
		t.Fatal("成功条目必须已落盘存储")
	}
	if got := a.ListTunnels(); len(got) != 2 {
		t.Fatalf("冲突条目不得落盘: want 2 tunnels (existing+8080), got %d (%+v)", len(got), got)
	}
}

// A2 配套：全部成功时 error 必须为 nil，返回列表与落盘一致（不因改造回归）。
func TestImportCommandAllSuccessReturnsNilError(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	ts, err := a.ImportCommand("ssh -N -L 5432:127.0.0.1:5432 -L 8080:127.0.0.1:80 prod")
	if err != nil {
		t.Fatalf("全部成功时 error 必须为 nil: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("want 2 tunnels, got %d", len(ts))
	}
	if got := a.ListTunnels(); len(got) != 2 {
		t.Fatalf("两条都应落盘: got %d", len(got))
	}
}
