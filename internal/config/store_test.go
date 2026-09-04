package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	cfg := &AppConfig{
		App: AppSettings{AutoReconnectDefault: true},
		Tunnels: []Tunnel{{
			ID: "abc123", Name: "prod-db", Mode: "local", Host: "prod-db",
			ListenBind: "127.0.0.1", ListenPort: 5432,
			TargetHost: "127.0.0.1", TargetPort: 5432,
			AutoReconnect: true, Enabled: false,
		}},
		RecentSFTP: []RecentSFTP{{Host: "prod-db", RemoteDir: "~/logs", LocalDir: "/tmp", TS: "2026-08-25T10:00:00Z"}},
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Tunnels) != 1 || got.Tunnels[0].Name != "prod-db" || got.Tunnels[0].Mode != "local" ||
		got.Tunnels[0].ListenPort != 5432 || got.Tunnels[0].TargetPort != 5432 {
		t.Fatalf("tunnel round-trip wrong: %+v", got.Tunnels)
	}
	if !got.App.AutoReconnectDefault {
		t.Fatal("auto_reconnect_default lost")
	}
}

// mixedWriterPrefixes reports whether tunnels come from more than one
// writer (ID scheme "g<N>-<M>"): a mix means two writes interleaved.
func mixedWriterPrefixes(tunnels []Tunnel) bool {
	prefixes := map[string]bool{}
	for _, tun := range tunnels {
		idx := strings.Index(tun.ID, "-")
		if idx <= 1 {
			return true
		}
		prefixes[tun.ID[:idx]] = true
	}
	return len(prefixes) > 1
}

// M11: 并发保存期间文件必须始终是"某次完整保存"的样子。O_TRUNC 直接覆写
// 会在 truncate→写回之间暴露空文件/半截 TOML/多写者混合内容；原子
// rename 则让读者只可能看到完整的旧文件或完整的文件。读端在保存期间
// 持续采样，写端 8×30 次保存（每条配置逐步变大，拉长写窗口）。
func TestSaveConfigConcurrentSavesNeverCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	stop := make(chan struct{})
	var torn int32
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(path); err != nil {
				continue // 首个保存尚未落盘
			}
			cfg, err := LoadConfig(path)
			if err != nil || len(cfg.Tunnels) == 0 || mixedWriterPrefixes(cfg.Tunnels) {
				atomic.StoreInt32(&torn, 1)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cfg := &AppConfig{App: AppSettings{AutoReconnectDefault: true}}
			for i := 0; i < 30; i++ {
				cfg.Tunnels = append(cfg.Tunnels, Tunnel{
					ID:   fmt.Sprintf("g%d-%d", n, i),
					Name: strings.Repeat(fmt.Sprintf("n%d-x", n), 40+i),
					Mode: "local", Host: "prod-db",
					ListenBind: "127.0.0.1", ListenPort: 5000 + n,
					TargetHost: "127.0.0.1", TargetPort: 80,
				})
				if err := SaveConfig(path, cfg); err != nil {
					t.Errorf("save g%d iter%d: %v", n, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	reader.Wait()

	if atomic.LoadInt32(&torn) != 0 {
		t.Fatal("reader observed an empty, undecodable or mixed config mid-save")
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("final file must be a complete decodable config: %v", err)
	}
	if len(got.Tunnels) == 0 {
		t.Fatal("final file has no tunnels — torn write")
	}
	if mixedWriterPrefixes(got.Tunnels) {
		t.Fatalf("final file mixes configs from multiple writers: %+v", got.Tunnels[:2])
	}
}

func TestSaveConfigAtomicNoTmpLeftAndContentMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sshore.toml")
	cfg1 := &AppConfig{App: AppSettings{AutoReconnectDefault: true}, Tunnels: []Tunnel{{ID: "a", Host: "h", Mode: "local", ListenPort: 1}}}
	if err := SaveConfig(path, cfg1); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg2 := &AppConfig{App: AppSettings{AutoReconnectDefault: true}, Tunnels: []Tunnel{
		{ID: "b", Host: "h", Mode: "local", ListenPort: 2},
		{ID: "c", Host: "h2", Mode: "dynamic", ListenPort: 1080},
	}}
	if err := SaveConfig(path, cfg2); err != nil {
		t.Fatalf("resave: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file after save: %s", e.Name())
		}
	}
	want, err := toml.Marshal(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content differs from saved config:\ngot:\n%s\nwant:\n%s", got, want)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 2 || loaded.Tunnels[0].ID != "b" || loaded.Tunnels[1].ID != "c" {
		t.Fatalf("round-trip wrong: %+v", loaded.Tunnels)
	}
}

func TestLoadConfigMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.toml")
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if !got.App.AutoReconnectDefault {
		t.Fatal("default should have auto_reconnect_default true")
	}
	if len(got.Tunnels) != 0 {
		t.Fatal("empty tunnels expected")
	}
}

// AppSettings.Normalize 必须把空主题/非法字号兜底为可用值，并夹紧字号区间。
func TestAppSettingsNormalizeDefaults(t *testing.T) {
	s := AppSettings{}
	s.Normalize()
	if s.Theme != "system" || s.FontScale != 1 {
		t.Fatalf("normalize defaults wrong: %+v", s)
	}
	high := AppSettings{FontScale: 3}
	high.Normalize()
	if high.FontScale != 2 {
		t.Fatalf("fontScale upper clamp wrong: %+v", high)
	}
	low := AppSettings{FontScale: 0.2}
	low.Normalize()
	if low.FontScale != 0.5 {
		t.Fatalf("fontScale lower clamp wrong: %+v", low)
	}
	neg := AppSettings{FontScale: -1}
	neg.Normalize()
	if neg.FontScale != 1 {
		t.Fatalf("non-positive fontScale should reset to 1: %+v", neg)
	}
}

// 出厂默认须包含界面设置：theme=system、fontScale=1、auto-start 开启。
func TestDefaultAppConfigHasUISettings(t *testing.T) {
	cfg := DefaultAppConfig()
	if cfg.App.Theme != "system" || cfg.App.FontScale != 1 || !cfg.App.AutoStartOnLaunch {
		t.Fatalf("default UI settings wrong: %+v", cfg.App)
	}
}

// 旧配置（无 theme/font/latin/cjk/auto_start 键）反序列化后必须保留 UI 默认值，
// 避免升级后应用以 0 字号/空主题启动。
func TestLoadConfigLegacyMissingUIFieldsKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	if err := os.WriteFile(path, []byte("[app]\nauto_reconnect_default = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Theme != "system" || cfg.App.FontScale != 1 || !cfg.App.AutoStartOnLaunch {
		t.Fatalf("legacy config should keep UI defaults: %+v", cfg.App)
	}
}

// 完整设置项往返：保存/加载后字段一致（含显式关闭 auto-start）。
func TestLoadSaveSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	cfg := &AppConfig{App: AppSettings{
		AutoReconnectDefault: true,
		Theme:                "light",
		FontScale:            1.15,
		LatinFont:            "inter",
		CJKFont:              "yahei",
		AutoStartOnLaunch:    false,
	}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	g := got.App
	if g.Theme != "light" || g.FontScale != 1.15 || g.LatinFont != "inter" || g.CJKFont != "yahei" || g.AutoStartOnLaunch != false {
		t.Fatalf("settings round-trip broken: %+v", g)
	}
}
