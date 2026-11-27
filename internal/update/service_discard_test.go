package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// F6 回归：重启自检恢复的 pending 只填 Latest/ReadyPath，s.rel 是零值。
// 用户丢弃后旧实现置 available（只看 Latest != ""），再点下载 → 零值 rel →
// PickArchive 报 ErrNoAsset「本平台暂无可用包」——误导文案的死胡同。
// 修复语义：rel 为零值时丢弃必须回 idle（需要重新检查），前端 idle 态
// 恰好提供「立即检查更新」按钮（utils/update.js actions()），不破坏 UI。
func TestDiscardResumedPendingGoesIdleNotAvailable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 残留 pending：版本高于当前 → Init 自检进入 ready（rel 不会被填充）。
	if err := os.WriteFile(filepath.Join(dir, "sshore.v0.7.0"), []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0",
		ExePath: exe,
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	if got := svc.Info().State; got != StateReady {
		t.Fatalf("自检应进入 ready, got %s", got)
	}
	svc.mu.Lock()
	tag := svc.rel.Tag
	svc.mu.Unlock()
	if tag != "" {
		t.Fatalf("夹具前提：恢复路径不得填充 rel, got %q", tag)
	}

	if err := svc.DiscardPending(); err != nil {
		t.Fatal(err)
	}
	info := svc.Info()
	if info.State != StateIdle {
		t.Fatalf("丢弃重启恢复的 pending（rel 零值）后必须回 idle 等待重新检查, got %s", info.State)
	}
	if info.ReadyPath != "" {
		t.Fatalf("ReadyPath 必须清空, got %q", info.ReadyPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "sshore.v0.7.0")); !os.IsNotExist(err) {
		t.Fatal("丢弃必须删掉待安装文件")
	}
	// 死胡同消除：idle 不允许直接下载（ErrBusy），而不是带零值 rel 进入
	// downloading 后报「本平台暂无可用包」。
	if err := svc.StartDownload(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("idle 态 StartDownload 必须 ErrBusy, got %v", err)
	}
}

// F6 守卫：正常下载后的丢弃（rel 已填充）仍回 available，可直接重试下载。
func TestDiscardAfterDownloadStillAvailable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) { o.ExePath = exe })
	svc.mu.Lock()
	svc.rel = Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: "sshore-v0.7.0-linux-amd64.tar.gz", URL: "https://github.com/x/y/releases/download/v0.7.0/a.tar.gz"},
		{Name: "checksums.txt", URL: "https://github.com/x/y/releases/download/v0.7.0/checksums.txt"},
	}}
	svc.info.State = StateReady
	svc.info.Latest = "v0.7.0"
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.DiscardPending(); err != nil {
		t.Fatal(err)
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("rel 已填充时丢弃仍应 available, got %s", got)
	}
}
