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
