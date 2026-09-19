package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanForNaming(t *testing.T) {
	exe := filepath.Join("/opt/sshore", "sshore")
	p := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 123, DefaultWait)
	if p.Pending != filepath.Join("/opt/sshore", "sshore.v0.7.0") {
		t.Fatalf("pending 名错误: %s", p.Pending)
	}
	if p.Backup != filepath.Join("/opt/sshore", "sshore.v0.6.0") {
		t.Fatalf("backup 名错误: %s", p.Backup)
	}
	if p.Sidecar != p.Pending+".sha256" {
		t.Fatalf("sidecar 名错误: %s", p.Sidecar)
	}
	if p.LogPath != filepath.Join("/opt/sshore", "sshore-update.log") {
		t.Fatalf("日志路径错误: %s", p.LogPath)
	}
	win := PlanFor("windows", "C:/app/sshore.exe", "v0.6.0", "v0.7.0", 1, DefaultWait)
	if filepath.Base(win.Pending) != "sshore.v0.7.0.exe" {
		t.Fatalf("windows pending 名错误: %s", win.Pending)
	}
}

func TestPlanForDescribeAndDevBackupNames(t *testing.T) {
	exe := "/opt/sshore/sshore"
	describe := PlanFor("linux", exe, "v0.6.0-80-gc2d2a36", "v0.7.0", 1, 5*time.Second)
	if describe.Backup != "/opt/sshore/sshore.v0.6.0-80-gc2d2a36" {
		t.Fatalf("describe 备份名错误: %s", describe.Backup)
	}
	if describe.Wait != 5*time.Second {
		t.Fatalf("wait 未透传: %v", describe.Wait)
	}
	dev := PlanFor("linux", exe, "dev", "v0.7.0", 1, DefaultWait)
	if !hasDevTimestamp(filepath.Base(dev.Backup)) {
		t.Fatalf("dev 备份名必须带时间戳: %s", dev.Backup)
	}
}

func hasDevTimestamp(name string) bool {
	const prefix = "sshore.dev-"
	if len(name) < len(prefix)+15 {
		return false
	}
	for _, r := range name[len(prefix):] {
		if r != '-' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func TestIsPendingNameUsesVersionOrder(t *testing.T) {
	cases := []struct {
		name, current string
		want          bool
	}{
		{"sshore.v0.7.0", "v0.6.0", true},
		{"sshore.v0.6.0", "v0.7.0", false},
		{"sshore.v0.6.0", "v0.6.0", false},
		{"sshore.v0.6.0-80-gc2d2a36", "v0.6.0", false},
		{"sshore.dev-20260919-101112", "v0.6.0", false},
		{"sshore.v0.7.0", "v0.6.0-80-gc2d2a36", true},
		{"sshore.exe", "v0.6.0", false},
		{"sshore-update.sh", "v0.6.0", false},
		{"sshore.v0.7.0.sha256", "v0.6.0", false},
	}
	for _, c := range cases {
		if got := IsPendingName(c.name, c.current); got != c.want {
			t.Errorf("IsPendingName(%q, %q) = %v, want %v", c.name, c.current, got, c.want)
		}
	}
}

func TestResumePending(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"sshore", "sshore.v0.6.0", "sshore.v0.7.0", "sshore.v0.7.0.sha256", "sshore-update.sh"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, ok := ResumePending(dir, "v0.6.0")
	if !ok {
		t.Fatal("应识别出待安装文件 sshore.v0.7.0")
	}
	if filepath.Base(p.Pending) != "sshore.v0.7.0" || p.Size != 0 {
		t.Fatalf("恢复计划错误: %+v", p)
	}
	if filepath.Base(p.Target) != "sshore" || p.ExeDir != dir {
		t.Fatalf("目标/目录错误: %+v", p)
	}
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir2, "sshore.v0.6.0"), []byte("x"), 0o755)
	if _, ok := ResumePending(dir2, "v0.7.0"); ok {
		t.Fatal("备份不能被当成待安装文件")
	}
}
