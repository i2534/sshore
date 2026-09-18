package sftp

import "testing"

func TestResolveTransportPrecedence(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	if got := resolveTransport(func() string { return "gosftp" }); got != KindGo {
		t.Fatalf("config 生效时 want KindGo, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "batch")
	if got := resolveTransport(func() string { return "gosftp" }); got != KindBatch {
		t.Fatalf("env 应覆盖 config, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "  GOSFTP ")
	if got := resolveTransport(func() string { return "batch" }); got != KindGo {
		t.Fatalf("env 应 trim+小写, got %v", got)
	}
}

func TestResolveTransportInvalidAndNilFallsBack(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "whatever")
	if got := resolveTransport(nil); got != defaultTransport {
		t.Fatalf("非法 env + nil 选择器应回落 defaultTransport, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	if got := resolveTransport(func() string { return "  " }); got != defaultTransport {
		t.Fatalf("空 config 应回落 defaultTransport, got %v", got)
	}
}

// TestDefaultTransportIsGo（Task 16）钉住内置默认 = gosftp：切默认是发布决策，
// 必须由测试断言而不是靠注释；同时钉住两个方向的环境变量覆盖仍然可用
// （batch = 回退旧后端，gosftp = 显式选新后端），避免某次重构把回退路径删掉。
func TestDefaultTransportIsGo(t *testing.T) {
	if defaultTransport != KindGo {
		t.Fatalf("Task 16 起内置默认必须是 KindGo（gosftp），got %v", defaultTransport)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	if got := resolveTransport(nil); got != KindGo {
		t.Fatalf("env 与 config 都为空时应落到 gosftp, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "batch")
	if got := resolveTransport(nil); got != KindBatch {
		t.Fatalf("env=batch 应可回退旧后端, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "gosftp")
	if got := resolveTransport(func() string { return "batch" }); got != KindGo {
		t.Fatalf("env=gosftp 应覆盖 config=batch, got %v", got)
	}
}
