package main

import (
	"path/filepath"
	stdsync "sync"
	"testing"

	"sshore/internal/config"
)

func TestUpdateBindingsAreSafeWithoutStartup(t *testing.T) {
	// 测试里直接构造的 App 没走 startup，updater 为 nil；绑定必须返回 disabled 快照而不是 panic。
	a := &App{}
	info := a.GetUpdateInfo()
	if info.State != "disabled" {
		t.Fatalf("state = %s, want disabled", info.State)
	}
	if _, err := a.CheckUpdate(true); err == nil {
		t.Fatal("未初始化时必须返回错误而不是 panic")
	}
	if err := a.StartUpdateDownload(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if err := a.ApplyUpdateAndRestart(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if a.CancelUpdateDownload() {
		t.Fatal("未初始化时取消应返回 false")
	}
}

// TestAppSettingsRaceWithUpdateSettings 覆盖评审 Important：更新服务 goroutine 读
// a.cfg.App（a.updateSettings 的 Config 回调）与 Wails 调用线程写 a.cfg.App
// （a.setAppSettings，即 SetSettings 的写入）必须经同一把锁。
// 用 go test . -race -run TestAppSettingsRaceWithUpdateSettings -count=5 验证。
func TestAppSettingsRaceWithUpdateSettings(t *testing.T) {
	a := &App{cfg: config.DefaultAppConfig()}

	var wg stdsync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	// 写侧：模拟 Wails 线程的 SetSettings 受保护写入（不落盘，避免测出真实配置）。
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			a.setAppSettings(config.AppSettings{
				Theme:                    "dark",
				UpdateCheckAuto:          i%2 == 0,
				UpdateCheckIntervalHours: i%24 + 1,
				UpdateSource:             "https://example.invalid/updates",
				UpdateSkippedVersion:     "1.2.3",
			})
			if got := a.appSettings().UpdateSkippedVersion; got != "1.2.3" {
				t.Errorf("读回跳过版本 = %q, want 1.2.3", got)
			}
		}
	}()
	// 读侧：模拟更新服务 goroutine 反复取配置子集。
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			_ = a.updateSettings()
		}
	}()
	close(start)
	wg.Wait()
}

// TestSaveConfigSnapshotRace 覆盖 fix round 2：saveConfig 必须先在读锁下复制 a.cfg，
// 再用副本序列化落盘。否则 Wails 线程 SetSettings 的整结构写（setAppSettings 写
// a.cfg.App）会与落盘路径对 a.cfg 的 TOML 序列化并发读写同一内存。
// 两侧都只用 t.TempDir() 下的临时配置路径，绝不触碰真实用户配置。
// 用 go test . -race -run TestSaveConfigSnapshotRace -count=5 验证。
func TestSaveConfigSnapshotRace(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		cfg:     config.DefaultAppConfig(),
		cfgPath: filepath.Join(dir, "sshore.toml"),
	}

	var wg stdsync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	// 写侧：模拟 Wails 线程的 SetSettings（整结构写 a.cfg.App + 落盘）。
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 300; i++ {
			if err := a.SetSettings(config.AppSettings{
				Theme:                    "dark",
				UpdateCheckIntervalHours: i%24 + 1,
				UpdateSource:             "https://example.invalid/updates",
				UpdateSkippedVersion:     "1.2.3",
			}); err != nil {
				t.Errorf("SetSettings: %v", err)
				return
			}
		}
	}()
	// 落盘侧：模拟更新 goroutine 的落盘入口，与写侧的 saveConfig 并发。
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 300; i++ {
			_ = a.saveConfig()
		}
	}()
	close(start)
	wg.Wait()
}
