package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sshkit/internal/config"
	"sshkit/internal/forward"
)

func TestCreateAndStartInvalidHost(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	err := a.CreateTunnel(config.Tunnel{ID: "x", Host: "-oevil", Mode: "local", ListenPort: 1})
	if err == nil {
		t.Fatal("expected invalid host error")
	}
}

func TestImportCommandCreatesTunnels(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	ts, err := a.ImportCommand("ssh -N -L 5432:127.0.0.1:5432 prod-db")
	if err != nil {
		t.Fatalf("import err: %v", err)
	}
	if len(ts) != 1 {
		t.Fatalf("want 1 tunnel got %d", len(ts))
	}
	got, ok := a.findTunnel(ts[0].ID)
	if !ok || got.Mode != "local" || got.ListenPort != 5432 {
		t.Fatalf("tunnel not stored: %+v", got)
	}
}

func TestSaveConfigPersistsTunnels(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshkit.toml")
	if err := a.CreateTunnel(config.Tunnel{ID: "abc", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.saveConfig(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(a.cfgPath); err != nil {
		t.Fatalf("config file should exist: %v", err)
	}
	got, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Tunnels) != 1 || got.Tunnels[0].ID != "abc" {
		t.Fatalf("tunnel not persisted: %+v", got.Tunnels)
	}
}

// H2: 配置文件存在但解析失败时，loadOrBackupConfig 必须原样备份原文件，
// 并返回可用的空配置 + 非 nil 错误（错误后续经事件展示给用户）。
func TestLoadOrBackupConfigCorruptBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sshkit.toml")
	garbage := []byte("this is [[ not valid toml = =")
	if err := os.WriteFile(path, garbage, 0600); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	cfg, err := a.loadOrBackupConfig(path)
	if err == nil {
		t.Fatal("expected error for corrupt config")
	}
	if cfg == nil {
		t.Fatal("must still return a usable config")
	}
	if len(cfg.Tunnels) != 0 {
		t.Fatalf("expected empty tunnels, got %+v", cfg.Tunnels)
	}
	// 原文件必须保持不动
	if data, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(data, garbage) {
		t.Fatalf("original config file was modified: %v", rerr)
	}
	// 必须存在 <path>.bak-<ts> 且内容与原文件一致
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var bakData []byte
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sshkit.toml.bak-") {
			bakData, rerr = os.ReadFile(filepath.Join(dir, e.Name()))
			if rerr != nil {
				t.Fatal(rerr)
			}
		}
	}
	if bakData == nil {
		t.Fatal("no .bak file created")
	}
	if !bytes.Equal(bakData, garbage) {
		t.Fatal("backup content differs from original")
	}
}

// H2: 文件不存在不算解析失败：返回默认配置、无错误、不产生备份。
func TestLoadOrBackupConfigMissingNoBackup(t *testing.T) {
	dir := t.TempDir()
	a := NewApp()
	cfg, err := a.loadOrBackupConfig(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg == nil || len(cfg.Tunnels) != 0 {
		t.Fatalf("expected default empty config, got %+v", cfg)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			t.Fatalf("unexpected backup file %q", e.Name())
		}
	}
}

// H2: startup 发现的加载错误在 Init 注入 emit 后以 error 事件发出（用户可见）。
func TestInitEmitsConfigLoadError(t *testing.T) {
	a := NewApp()
	a.cfgLoadErr = errors.New("配置文件损坏，已备份到 /tmp/x.bak-1")
	var got []forward.Event
	a.Init(func(e forward.Event) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("want exactly 1 event, got %d: %+v", len(got), got)
	}
	if got[0].Level != "error" || got[0].SourceType != "system" || got[0].SourceID != "app" {
		t.Fatalf("unexpected event: %+v", got[0])
	}
	if !strings.Contains(got[0].Message, "已备份") {
		t.Fatalf("event message should mention backup: %+v", got[0])
	}
	// 无加载错误时不得发事件
	a2 := NewApp()
	a2.Init(func(forward.Event) { t.Fatal("unexpected event without load error") })
}

func TestUpdateTunnelReplacesById(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshkit.toml")
	if err := a.CreateTunnel(config.Tunnel{ID: "abc", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432}); err != nil {
		t.Fatal(err)
	}
	upd := config.Tunnel{ID: "abc", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 9090, TargetHost: "127.0.0.1", TargetPort: 9090}
	if err := a.UpdateTunnel(upd); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(a.cfg.Tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(a.cfg.Tunnels))
	}
	if a.cfg.Tunnels[0].ListenPort != 9090 {
		t.Fatalf("port not updated: %+v", a.cfg.Tunnels[0])
	}
}

func TestDeleteTunnelRemovesAndStops(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshkit.toml")
	if err := a.CreateTunnel(config.Tunnel{ID: "abc", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteTunnel("abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(a.cfg.Tunnels) != 0 {
		t.Fatalf("expected 0 tunnels, got %+v", a.cfg.Tunnels)
	}
	if _, ok := a.findTunnel("abc"); ok {
		t.Fatal("tunnel should be removed")
	}
}
