package main

import (
	"bytes"
	"errors"
	"net"
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

// M10: AutoStartEnabled 不得首败即止：t1（非法 host）快速失败后，
// t2（合法 host 但端口被占用，预检阶段失败）仍须被尝试；
// 返回的汇总错误应包含两个隧道，且 t2 的状态/事件证明其被真正启动过
// （端口预检保证两个隧道都不产生真实 ssh 进程）。
func TestAutoStartEnabledContinuesAfterFirstFailure(t *testing.T) {
	a := NewApp()
	var events []forward.Event
	a.Init(func(e forward.Event) { events = append(events, e) })
	a.cfgPath = filepath.Join(t.TempDir(), "sshkit.toml")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	a.cfg = &config.AppConfig{Tunnels: []config.Tunnel{
		{ID: "t1", Name: "bad", Host: "-evil", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1, Enabled: true},
		{ID: "t2", Name: "good", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: busyPort, TargetHost: "127.0.0.1", TargetPort: 80, Enabled: true},
	}}

	err = a.AutoStartEnabled()
	if err == nil {
		t.Fatal("expected joined error when some tunnels fail")
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Fatalf("error should mention failed tunnel t1: %v", err)
	}
	if !strings.Contains(err.Error(), "t2") {
		t.Fatalf("error should mention failed tunnel t2: %v", err)
	}
	if st := a.forward.State("t2"); st != forward.StateError {
		t.Fatalf("t2 should have been attempted and end in error state, got %s", st)
	}
	appLevel := false
	for _, e := range events {
		if e.SourceType == "tunnel" && e.SourceID == "t2" && e.Level == "error" && strings.Contains(e.Message, "自动启动") {
			appLevel = true
		}
	}
	if !appLevel {
		t.Fatalf("expected app-level 自动启动 failure event for t2, got %+v", events)
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

// M9: DeleteLocal 纵深防御——拒绝删除用户 home 目录本身，
// 避免 JS 侧传入的危险路径直接落到 os.RemoveAll。
// HOME 重定向到隔离临时目录，防止未加防护的实现误删真实 home。
// "/" 与 Windows 盘符根的拒绝在 TestCheckDeletablePath 中单元级覆盖
// （未加防护的实现会对真实 "/" 发起全文件系统遍历，不可在行为测试中直接执行）。
func TestDeleteLocalRejectsHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := NewApp()
	a.Init(func(forward.Event) {})

	err := a.DeleteLocal(home)
	if err == nil {
		t.Fatal("deleting the home dir itself should be rejected")
	}
	if !strings.Contains(err.Error(), "拒绝删除") {
		t.Fatalf("rejection of home should carry a clear message: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("home dir must survive: %v", err)
	}
}

// M9: 路径校验单元的拒绝/放行边界（"/"、Windows 盘符根、home 本身拒绝；
// home 子路径与盘符下普通路径放行，保持"仅精确匹配"的最小防护）。
func TestCheckDeletablePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, p := range []string{"/", "C:\\", "C:/", "c:\\"} {
		err := checkDeletablePath(p)
		if err == nil {
			t.Fatalf("%q should be rejected", p)
		}
		if !strings.Contains(err.Error(), "拒绝删除") {
			t.Fatalf("%q rejection should carry a clear message: %v", p, err)
		}
	}
	if err := checkDeletablePath(home); err == nil {
		t.Fatal("home dir itself should be rejected")
	}
	for _, p := range []string{`C:\Users\foo`, home + string(filepath.Separator) + "sub", t.TempDir()} {
		if err := checkDeletablePath(p); err != nil {
			t.Fatalf("%q should be allowed: %v", p, err)
		}
	}
}

func TestDeleteLocalRemovesNormalTempDir(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.MkdirAll(filepath.Join(dir, "inner"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inner", "f.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteLocal(dir); err != nil {
		t.Fatalf("deleting a normal dir should succeed: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir should be removed, stat err=%v", err)
	}
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
