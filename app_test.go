package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sftp"
	"sshore/internal/sync"
)

// TestMain 把 os.UserConfigDir() 锚定到临时目录，作为「测试绝不可写用户真实配置」的
// 兜底：任何遗漏 cfgPath 的用例都会写到这里，而不是 ~/.config/sshore/sshore.toml。
// 这不是修具体用例的替代品——TestImportCommandCreatesTunnels 已显式设置 cfgPath。
// 注意：os.UserConfigDir 在 Windows 读 %AppData%、unix 读 $XDG_CONFIG_HOME，
// 所以两处都要设；darwin 读 ~/Library/Application Support，这里不覆盖（避免动 HOME）。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sshore-test-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建测试配置目录失败:", err)
		os.Exit(1)
	}
	if runtime.GOOS == "windows" {
		_ = os.Setenv("APPDATA", dir)
	} else {
		_ = os.Setenv("XDG_CONFIG_HOME", dir)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// appWithFakeSFTP 返回一个 sftp 控制器由假 runner 支撑的 App：
// 所有 sftp 调用成功，stdout 为给定的固定输出（不启动真实进程）。
func appWithFakeSFTP(t *testing.T, stdout string) *App {
	t.Helper()
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	a.sftp = sftp.NewCtrl(func(name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{Stdout: stdout, ExitCode: 0}, nil
	}, func(forward.Event) {})
	return a
}

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
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	a.cfg = &config.AppConfig{
		App: config.AppSettings{AutoReconnectDefault: true, AutoStartOnLaunch: true},
		Tunnels: []config.Tunnel{
			{ID: "t1", Name: "bad", Host: "-evil", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1, Enabled: true},
			{ID: "t2", Name: "good", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: busyPort, TargetHost: "127.0.0.1", TargetPort: 80, Enabled: true},
		},
	}

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

// 设置里的 AutoStartOnLaunch=false 时，AutoStartEnabled 必须整体跳过：
// 不启动任何隧道、不发任何事件、不产生错误。
func TestAutoStartEnabledSkippedWhenDisabled(t *testing.T) {
	a := NewApp()
	var events []forward.Event
	a.Init(func(e forward.Event) { events = append(events, e) })
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	a.cfg = &config.AppConfig{
		App: config.AppSettings{AutoReconnectDefault: true, AutoStartOnLaunch: false},
		Tunnels: []config.Tunnel{
			{ID: "t1", Name: "prod", Host: "prod-db", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432, Enabled: true},
		},
	}
	if err := a.AutoStartEnabled(); err != nil {
		t.Fatalf("auto-start should be skipped without error, got %v", err)
	}
	if st := a.forward.State("t1"); st != forward.StateStopped {
		t.Fatalf("t1 must not have been started when disabled, state=%q", st)
	}
	if len(events) != 0 {
		t.Fatalf("no events expected when auto-start disabled, got %+v", events)
	}
}

// GetAppInfo 必须返回非空的应用名与版本/仓库（默认值或构建注入值）。
func TestGetAppInfo(t *testing.T) {
	a := NewApp()
	info := a.GetAppInfo()
	if info.Name != "SSHore" {
		t.Fatalf("app name: %q", info.Name)
	}
	if info.Version == "" || info.Repo == "" {
		t.Fatalf("version/repo should be non-empty: %+v", info)
	}
}

// 窗口标题必须是「SSHore <版本>」，随构建注入的 Version 一起变化。
func TestAppTitle(t *testing.T) {
	if got, want := appTitle(), "SSHore "+Version; got != want {
		t.Fatalf("app title: got %q, want %q", got, want)
	}
	if got := appTitle(); !strings.HasPrefix(got, "SSHore ") {
		t.Fatalf("app title should start with %q: %q", "SSHore ", got)
	}
}

// SyncWindowBackground 在 ctx 未设置（启动前/单元测试）时必须安全无操作，不 panic。
func TestSyncWindowBackgroundNoCtx(t *testing.T) {
	a := NewApp() // a.ctx == nil
	a.SyncWindowBackground("light")
	a.SyncWindowBackground("dark")
}

// cfg 为 nil 时 GetSettings 应返回安全默认（theme=system, fontScale=1, auto-start 开启）。
func TestGetSettingsNilCfgReturnsDefaults(t *testing.T) {
	a := NewApp()
	s := a.GetSettings()
	if s.Theme != "system" || s.FontScale != 1 || !s.AutoStartOnLaunch {
		t.Fatalf("unexpected default settings: %+v", s)
	}
}

// SetSettings 归一化并持久化：空 theme→system、非法 fontScale→1、其余字段原样落盘。
func TestSetSettingsPersistsAndNormalizes(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	in := config.AppSettings{LatinFont: "inter", CJKFont: "yahei", AutoStartOnLaunch: false}
	if err := a.SetSettings(in); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := a.GetSettings()
	if got.Theme != "system" || got.FontScale != 1 || got.LatinFont != "inter" || got.CJKFont != "yahei" || got.AutoStartOnLaunch != false {
		t.Fatalf("normalize/persist wrong: %+v", got)
	}
	loaded, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.App.Theme != "system" || loaded.App.FontScale != 1 || loaded.App.LatinFont != "inter" || loaded.App.CJKFont != "yahei" {
		t.Fatalf("persisted settings wrong: %+v", loaded.App)
	}
}

// 非法主题值必须回退到 system。
func TestSetSettingsInvalidThemeFallsBackToSystem(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	if err := a.SetSettings(config.AppSettings{Theme: "blue", FontScale: 1.15}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := a.GetSettings().Theme; got != "system" {
		t.Fatalf("invalid theme should fall back to system, got %q", got)
	}
}

func TestImportCommandCreatesTunnels(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	// ImportCommand→CreateTunnel 会 saveConfig；不设 cfgPath 时它会落到
	// config.DefaultConfigPath()，即用户真实的 ~/.config/sshore/sshore.toml。
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
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
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
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
	path := filepath.Join(dir, "sshore.toml")
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
		if strings.HasPrefix(e.Name(), "sshore.toml.bak-") {
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

// M7: ListHostsDetailed 端到端——HOME 重定向让 FindSSHConfigPath 命中隔离配置，
// PATH 前置 shim 让默认 exec 包装（真实 ssh 二进制）也解析同一份隔离配置
// （OpenSSH 从 passwd 而非 $HOME 解析 ~/.ssh/config，故需 -F 注入）。
// 配置块未设 User 时库解析为空——User 非空即可证明 ssh -G 权威富化真实生效。
func TestListHostsDetailedEnrichesViaSSH_G(t *testing.T) {
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh binary not available")
	}
	if runtime.GOOS == "windows" {
		t.Skip("该用例依赖 POSIX shell shim 与 HOME 重定向；Windows 的 FindSSHConfigPath 走 USERPROFILE，shim 也无法执行")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("Host prod-db\n  HostName 10.9.9.9\n  Port 2222\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	shim := fmt.Sprintf("#!/bin/sh\nexec %s -F %q \"$@\"\n", realSSH, cfgPath)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(shim), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	a := NewApp()
	a.Init(func(forward.Event) {})
	hosts := a.ListHostsDetailed()
	var prod *config.Host
	for i := range hosts {
		if hosts[i].Alias == "prod-db" {
			prod = &hosts[i]
		}
	}
	if prod == nil {
		t.Fatalf("prod-db missing from %d hosts: %+v", len(hosts), hosts)
	}
	if prod.HostName != "10.9.9.9" || prod.Port != 2222 {
		t.Fatalf("host fields wrong: %+v", prod)
	}
	if prod.User == "" {
		t.Fatal("user should be enriched by ssh -G (library has none)")
	}
}

// M9: DeleteLocal 纵深防御——拒绝删除用户 home 目录本身，
// 避免 JS 侧传入的危险路径直接落到 os.RemoveAll。
// HOME 重定向到隔离临时目录，防止未加防护的实现误删真实 home。
// "/" 与 Windows 盘符根的拒绝在 TestCheckDeletablePath 中单元级覆盖
// （未加防护的实现会对真实 "/" 发起全文件系统遍历，不可在行为测试中直接执行）。
func TestDeleteLocalRejectsHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		// Windows 的 os.UserHomeDir 读 USERPROFILE，只改 HOME 不生效。
		t.Setenv("USERPROFILE", home)
	}
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
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	// 文件系统根按平台取：Windows 是 "\\"，Unix 是 "/"。
	for _, p := range []string{string(filepath.Separator), "C:\\", "C:/", "c:\\"} {
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
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
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

// M7: 远端转发端口冲突必须在创建时拒绝（spec §4.5 对所有 mode 生效，
// 与规则自身的 mode 无关）：错误信息点名冲突规则，且列表保持不变。
func TestCreateTunnelRejectsRemoteConflict(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	first := config.Tunnel{ID: "t1", Name: "prod-6000", Host: "prod-db", Mode: "remote",
		ListenBind: "127.0.0.1", ListenPort: 6000, TargetHost: "127.0.0.1", TargetPort: 80}
	if err := a.CreateTunnel(first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// 同 (host, bind, port)、不同 mode、不同 ID → 必须拒绝
	dup := config.Tunnel{ID: "t2", Name: "dup", Host: "prod-db", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 6000, TargetHost: "127.0.0.1", TargetPort: 80}
	err := a.CreateTunnel(dup)
	if err == nil {
		t.Fatal("duplicate (host,bind,port) must be rejected")
	}
	if !strings.Contains(err.Error(), "prod-6000") {
		t.Fatalf("error should name the conflicting rule: %v", err)
	}
	if len(a.cfg.Tunnels) != 1 {
		t.Fatalf("list must be unchanged after rejection, got %d tunnels: %+v", len(a.cfg.Tunnels), a.cfg.Tunnels)
	}
}

// M7: UpdateTunnel 保留自身 (host, bind, port) 必须放行——
// CheckRemoteConflict 会跳过 e.ID==candidate.ID，改名/改目标不应被自己的键拦住。
func TestUpdateTunnelKeepingOwnKeySucceeds(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	orig := config.Tunnel{ID: "t1", Name: "old-name", Host: "prod-db", Mode: "remote",
		ListenBind: "127.0.0.1", ListenPort: 6000, TargetHost: "127.0.0.1", TargetPort: 80}
	if err := a.CreateTunnel(orig); err != nil {
		t.Fatalf("create: %v", err)
	}
	upd := orig
	upd.Name = "new-name"
	upd.TargetPort = 8080
	if err := a.UpdateTunnel(upd); err != nil {
		t.Fatalf("update keeping own key must succeed: %v", err)
	}
	if len(a.cfg.Tunnels) != 1 || a.cfg.Tunnels[0].Name != "new-name" || a.cfg.Tunnels[0].TargetPort != 8080 {
		t.Fatalf("update not applied: %+v", a.cfg.Tunnels)
	}
}

// M7: 相同 (bind, port) 但不同 host 不冲突，必须允许创建。
func TestCreateTunnelSamePortDifferentHostAllowed(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	for _, host := range []string{"prod-db", "staging"} {
		tl := config.Tunnel{ID: host, Name: host, Host: host, Mode: "local",
			ListenBind: "127.0.0.1", ListenPort: 6000, TargetHost: "127.0.0.1", TargetPort: 80}
		if err := a.CreateTunnel(tl); err != nil {
			t.Fatalf("same port on different host %q must be allowed: %v", host, err)
		}
	}
	if len(a.cfg.Tunnels) != 2 {
		t.Fatalf("want 2 tunnels, got %d", len(a.cfg.Tunnels))
	}
}

// startup 必须把旧 recent_sftp 迁移成新字段并显式落盘（裁决：读函数不写盘），
// 且迁移后旧字段不得再被写回。用 TestMain 锚定的临时配置目录，不碰真实 ~/.config。
func TestStartupMigratesLegacyRecentsToDisk(t *testing.T) {
	p, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveConfig(p, &config.AppConfig{
		RecentSFTP: []config.RecentSFTP{{Host: "prod", RemoteDir: "/x", LocalDir: "/y", TS: "t"}},
	}); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.startup(context.Background())
	back, err := config.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !back.LegacyMigrated || len(back.RemoteRecent) != 1 || len(back.LocalRecent) != 1 {
		t.Fatalf("startup 未迁移: %+v", back)
	}
	if len(back.RecentSFTP) != 0 {
		t.Fatalf("旧字段不得被写回: %+v", back.RecentSFTP)
	}
}

// P2: SftpGet 成功后把 (host, dir(remote)) 记入 RemoteRecent、dir(local) 记入
// LocalRecent，且持久化落盘；旧 recent_sftp 不再被写入（spec §10.2）。
func TestSftpGetRecordsRecent(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpGet("prod-db", "alice", "/var/log/app.log", "/tmp/dl/app.log"); err != nil {
		t.Fatalf("get: %v", err)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 0 {
		t.Fatalf("旧字段不得再被写回: %+v", cfg.RecentSFTP)
	}
	if len(cfg.RemoteRecent) != 1 {
		t.Fatalf("want 1 remote recent, got %d: %+v", len(cfg.RemoteRecent), cfg.RemoteRecent)
	}
	e := cfg.RemoteRecent[0]
	if wantR := path.Dir("/var/log/app.log"); e.Host != "prod-db" || e.Path != wantR {
		t.Fatalf("wrong remote entry: %+v", e)
	}
	if _, perr := time.Parse(time.RFC3339, e.TS); perr != nil || e.TS == "" {
		t.Fatalf("TS must be RFC3339, got %q", e.TS)
	}
	if len(cfg.LocalRecent) != 1 {
		t.Fatalf("want 1 local recent, got %d: %+v", len(cfg.LocalRecent), cfg.LocalRecent)
	}
	if wantL := filepath.Dir("/tmp/dl/app.log"); cfg.LocalRecent[0].Path != wantL {
		t.Fatalf("wrong local entry: %+v", cfg.LocalRecent[0])
	}
}

// P2: SftpPut 成功后同样写双侧新字段（remote/local 目录与 Get 对称）。
func TestSftpPutRecordsRecent(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpPut("prod-db", "alice", "/tmp/dl/app.log", "/var/log/app.log"); err != nil {
		t.Fatalf("put: %v", err)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 0 {
		t.Fatalf("旧字段不得再被写回: %+v", cfg.RecentSFTP)
	}
	if len(cfg.RemoteRecent) != 1 {
		t.Fatalf("want 1 remote recent, got %d: %+v", len(cfg.RemoteRecent), cfg.RemoteRecent)
	}
	if wantR := path.Dir("/var/log/app.log"); cfg.RemoteRecent[0].Host != "prod-db" || cfg.RemoteRecent[0].Path != wantR {
		t.Fatalf("wrong remote entry: %+v", cfg.RemoteRecent[0])
	}
	if len(cfg.LocalRecent) != 1 || cfg.LocalRecent[0].Path != filepath.Dir("/tmp/dl/app.log") {
		t.Fatalf("wrong local recents: %+v", cfg.LocalRecent)
	}
}

// P2: SftpHome 成功后只记远端 (host, home)，local 侧为空 ⇒ LocalRecent 不落条目。
func TestSftpHomeRecordsRecent(t *testing.T) {
	a := appWithFakeSFTP(t, "sftp> pwd\nRemote working directory: /home/alice\n")
	home, err := a.SftpHome("prod-db")
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if home != "/home/alice" {
		t.Fatalf("wrong home %q", home)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 0 {
		t.Fatalf("旧字段不得再被写回: %+v", cfg.RecentSFTP)
	}
	if len(cfg.RemoteRecent) != 1 {
		t.Fatalf("want 1 remote recent, got %d: %+v", len(cfg.RemoteRecent), cfg.RemoteRecent)
	}
	if e := cfg.RemoteRecent[0]; e.Host != "prod-db" || e.Path != "/home/alice" {
		t.Fatalf("wrong remote entry: %+v", e)
	}
	if len(cfg.LocalRecent) != 0 {
		t.Fatalf("无本地目录时不得写 LocalRecent: %+v", cfg.LocalRecent)
	}
}

// P2: 重复记录同一 (host, path) 时旧条目被移除、新条目置顶；本地侧同理按 path 去重。
func TestRecordRecentSFTPDedupMovesToFront(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpGet("h1", "", "/a/x", "/l1/x"); err != nil {
		t.Fatal(err)
	}
	if err := a.SftpGet("h2", "", "/b/y", "/l2/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.SftpGet("h1", "", "/a/x", "/l1/x"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RemoteRecent) != 2 {
		t.Fatalf("want 2 remote recents (dedup), got %d: %+v", len(cfg.RemoteRecent), cfg.RemoteRecent)
	}
	if cfg.RemoteRecent[0].Host != "h1" || cfg.RemoteRecent[1].Host != "h2" {
		t.Fatalf("re-recorded entry must move to front: %+v", cfg.RemoteRecent)
	}
	if len(cfg.LocalRecent) != 2 {
		t.Fatalf("want 2 local recents (dedup), got %d: %+v", len(cfg.LocalRecent), cfg.LocalRecent)
	}
	// 本地路径的 dir 由 filepath.Dir 推导（Linux = /l1、Windows = \l1）⇒ 期望值同源推导
	if wantL1, wantL2 := filepath.Dir("/l1/x"), filepath.Dir("/l2/y"); cfg.LocalRecent[0].Path != wantL1 || cfg.LocalRecent[1].Path != wantL2 {
		t.Fatalf("local recents must dedupe and move to front: %+v", cfg.LocalRecent)
	}
}

// 回归护栏（Windows 上才有分辨力）：远端是 POSIX 语义，远端最近位置里**绝不能出现反斜杠**。
// 旧实现用 filepath.Dir，Windows 上 filepath.Dir("/a/b/c.txt") == "\a\b" ⇒ 本用例会在
// CI 的 go-windows job 上变红；Linux 上 path.Dir 与 filepath.Dir 对 POSIX 输入等价，
// 所以这条在 Linux 是"恒真"的护栏，真正的判别力在 Windows。
func TestRemoteRecentNeverContainsBackslash(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpGet("prod", "", "/a/b/c.txt", "/l/c.txt"); err != nil {
		t.Fatal(err)
	}
	if len(a.cfg.RemoteRecent) != 1 {
		t.Fatalf("want 1 remote recent, got %+v", a.cfg.RemoteRecent)
	}
	if got := a.cfg.RemoteRecent[0].Path; strings.Contains(got, `\`) {
		t.Fatalf("远端路径必须是 POSIX 语义，不得出现反斜杠: %q", got)
	}
	if got := a.cfg.RemoteRecent[0].Path; got != "/a/b" {
		t.Fatalf("远端目录应为 POSIX 的 /a/b，got %q", got)
	}
}

// P2: 最近使用列表上限 20 条，最旧的被挤出；本地侧按 path 去重（25 次同目录只剩 1 条）。
func TestRecordRecentSFTPCapsAtTwenty(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	for i := 0; i < 25; i++ {
		host := "host" + string(rune('a'+i))
		if err := a.SftpGet(host, "", "/r"+string(rune('0'+i)), "/local"); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RemoteRecent) != 20 {
		t.Fatalf("want cap 20, got %d: %+v", len(cfg.RemoteRecent), cfg.RemoteRecent)
	}
	// 每轮 host 不同 ⇒ (host, "/") 全不相同；最新在前，末尾是被挤剩下的第 6 新（i=5 → hostf）。
	if cfg.RemoteRecent[0].Host != "hosty" || cfg.RemoteRecent[19].Host != "hostf" {
		t.Fatalf("newest-first order broken: %+v", cfg.RemoteRecent)
	}
	// 注意 "/local" 是单段路径 ⇒ filepath.Dir("/local") 在 Linux 是 "/"、在 Windows 是 "\"；
	// 25 次都记同一个值 ⇒ 去重成 1 条（期望值用同一个变换推导，避免平台差异）。
	if wantL := filepath.Dir("/local"); len(cfg.LocalRecent) != 1 || cfg.LocalRecent[0].Path != wantL {
		t.Fatalf("同目录 25 次必须去重成 1 条: %+v", cfg.LocalRecent)
	}
}

// P2/T9：ListRecentSFTP 绑定已随 T9 移除（spec §3 决策 20），原用例中依赖它的
// "空切片而非 nil" 断言一并退场；这里保留原用例最有价值的部分——锁定
// 「旧字段 recent_sftp 不再被写入」且「最近位置落在 RemoteRecent 且最新在前」。
func TestRecordRecentWritesNewFieldsOnly(t *testing.T) {
	a3 := appWithFakeSFTP(t, "")
	for _, host := range []string{"h1", "h2", "h3"} {
		if err := a3.SftpGet(host, "", "/r/"+host, "/l/"+host); err != nil {
			t.Fatal(err)
		}
	}
	// 旧字段不再被写入：落盘后 recent_sftp 必须为空。
	cfg3, err := config.LoadConfig(a3.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg3.RecentSFTP) != 0 {
		t.Fatalf("旧字段不得再被写回: %+v", cfg3.RecentSFTP)
	}
	// 最近位置落在新字段：RemoteRecent 最新在前。
	if len(cfg3.RemoteRecent) != 3 {
		t.Fatalf("want 3 remote recents, got %d: %+v", len(cfg3.RemoteRecent), cfg3.RemoteRecent)
	}
	want := []string{"h3", "h2", "h1"}
	for i, h := range want {
		if cfg3.RemoteRecent[i].Host != h {
			t.Fatalf("index %d: want %s got %+v", i, h, cfg3.RemoteRecent[i])
		}
	}
	// 内存里的旧字段同样应为空（recordRecentSFTP 只写新字段）。
	if len(a3.cfg.RecentSFTP) != 0 {
		t.Fatalf("内存中旧字段也不应被写入: %+v", a3.cfg.RecentSFTP)
	}
}

func TestDeleteTunnelRemovesAndStops(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
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

// 自动重连配套：TunnelStates 暴露各隧道运行态（id → state 字符串），
// 前端据此渲染四态圆点。用非法 host 路径制造一条 error 态条目（不产生真实 ssh 进程）。
func TestTunnelStates(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	if err := a.forward.Start(config.Tunnel{ID: "s1", Host: "-bad", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1}); err == nil {
		t.Fatal("invalid host must fail")
	}
	got := a.TunnelStates()
	if got["s1"] != "error" {
		t.Fatalf(`states["s1"]=%q want "error"`, got["s1"])
	}
}

// 空目标主机必须被创建/编辑拒绝——历史坏规则(
// `-L 127.0.0.1:23080::3080` → ssh 解析空主机失败 → 连接即 RST)由此防止复现。
func TestCreateTunnelRejectsEmptyTargetHost(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	err := a.CreateTunnel(config.Tunnel{ID: "x", Name: "DSH", Host: "ai", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 23080, TargetPort: 3080})
	if err == nil {
		t.Fatal("empty target host must be rejected on create")
	}
	if !strings.Contains(err.Error(), "target host") {
		t.Fatalf("error should mention target host: %v", err)
	}
	if len(a.cfg.Tunnels) != 0 {
		t.Fatalf("rejected tunnel must not be stored: %+v", a.cfg.Tunnels)
	}
}

func TestUpdateTunnelRejectsEmptyTargetHost(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")
	ok := config.Tunnel{ID: "abc", Host: "prod-db", Mode: "local",
		ListenBind: "127.0.0.1", ListenPort: 5432, TargetHost: "127.0.0.1", TargetPort: 5432}
	if err := a.CreateTunnel(ok); err != nil {
		t.Fatalf("create: %v", err)
	}
	bad := ok
	bad.TargetHost = ""
	if err := a.UpdateTunnel(bad); err == nil {
		t.Fatal("empty target host must be rejected on update")
	}
	if got := a.cfg.Tunnels[0].TargetHost; got != "127.0.0.1" {
		t.Fatalf("rejected update must not mutate stored rule, got %q", got)
	}
}

// 绑定契约：空切片而非 nil；创建时校验会拒绝坏规则。
func TestSyncRuleBindingsContract(t *testing.T) {
	a := newTestApp(t)
	if got := a.ListSyncRules(); got == nil {
		t.Fatal("ListSyncRules 必须返回空切片而不是 nil")
	}
	if got := a.SyncRuleConflicts("nope"); got == nil {
		t.Fatal("SyncRuleConflicts 必须返回空切片而不是 nil")
	}
	if got := a.SyncRuleStates(); got == nil {
		t.Fatal("SyncRuleStates 必须返回空 map 而不是 nil")
	}
	if _, err := a.CreateSyncRule(config.SyncRule{Host: "-bad", Kind: "dir", RemotePath: "/r", LocalPath: "/l"}); err == nil {
		t.Fatal("坏规则必须被拒绝")
	}
}

// 精确重复的规则必须被拒绝（对齐 CheckRemoteConflict 的做法）。
func TestCreateSyncRuleRejectsExactDuplicate(t *testing.T) {
	a := newTestApp(t)
	r := config.SyncRule{Host: "prod-01", Kind: "dir", RemotePath: "/r", LocalPath: "/l", PollIntervalS: 5}
	created, err := a.CreateSyncRule(r)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("创建时必须生成 ID")
	}
	if _, err := a.CreateSyncRule(r); err == nil {
		t.Fatal("同 host/remote/local/kind 的重复规则必须被拒绝")
	}
}

// newTestApp 构造隔离的 App：配置与状态都落在临时目录。
func newTestApp(t *testing.T) *App {
	t.Helper()
	a := &App{cfg: config.DefaultAppConfig(), cfgPath: filepath.Join(t.TempDir(), "sshore.toml")}
	a.sync = sync.NewCtrl(sync.Deps{StateDir: t.TempDir()})
	return a
}

// StopSyncRule 必须幂等：对 Enabled=true 但引擎未运行的规则调用后应返回 nil，
// 并把持久化的 Enabled 置回 false。否则该规则无法关闭、下次启动仍会被自动拉起。
func TestStopSyncRuleClearsEnabledWhenEngineNotRunning(t *testing.T) {
	a := newTestApp(t)
	a.cfg.Syncs = []config.SyncRule{{
		ID: "rule-x", Host: "prod-01", Kind: "dir",
		RemotePath: "/r", LocalPath: "/l", PollIntervalS: 5, Enabled: true,
	}}
	if err := a.StopSyncRule("rule-x"); err != nil {
		t.Fatalf("未运行的规则 StopSyncRule 必须返回 nil，得到 %v", err)
	}
	got, ok := a.findSyncRule("rule-x")
	if !ok {
		t.Fatal("StopSyncRule 不应删除规则")
	}
	if got.Enabled {
		t.Fatal("StopSyncRule 后持久化的 Enabled 必须为 false")
	}
	if a.cfg.Syncs[0].Enabled {
		t.Fatal("StopSyncRule 后 a.cfg.Syncs 的 Enabled 必须为 false")
	}
}

// I3（配套）：localStateOf 只允许把 fs.ErrNotExist 当成"本地不存在"；
// 其它 stat 错误（此例为自指符号链接的 ELOOP）必须上抛，绝不能静默返回 Exists=false。
func TestLocalStateOfSurfacesNonNotExistError(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "loop.txt")
	if err := os.Symlink("loop.txt", link); err != nil {
		t.Skipf("无法创建符号链接（环境不支持）: %v", err)
	}
	cfg := &config.AppConfig{Syncs: []config.SyncRule{{
		ID: "r", Host: "h", Kind: "dir", RemotePath: "/r", LocalPath: dir,
	}}}
	if _, err := localStateOf(cfg, "r", "loop.txt"); err == nil {
		t.Fatal("非 NotExist 的 stat 错误必须上抛，不能当成『本地不存在』")
	}
	st, err := localStateOf(cfg, "r", "missing.txt")
	if err != nil || st.Exists {
		t.Fatalf("真正不存在应返回 Exists=false 且无错误: st=%+v err=%v", st, err)
	}
}

// 附加项 2：OnShutdown 可能在 Init 之前被调用（Wails 生命周期边界），
// a.cfg / a.sync / a.forward / a.sftp 为 nil 时必须安全返回而不是 panic。
func TestOnShutdownBeforeInitDoesNotPanic(t *testing.T) {
	a := NewApp()
	a.OnShutdown()
}

// RetrySyncRuleFailures 对未运行的规则必须返回错误（绑定契约）。
func TestRetrySyncRuleFailuresRejectsNonRunningRule(t *testing.T) {
	a := newTestApp(t)
	if err := a.RetrySyncRuleFailures("nope"); err == nil {
		t.Fatal("未运行的规则必须返回错误")
	}
}

func TestCopyLocalRejectsSubtree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	if err := os.MkdirAll(filepath.Join(src, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if err := a.CopyLocal(src, filepath.Join(src, "inner", "copy")); err == nil {
		t.Fatal("expected rejection when dst is inside src")
	}
}

func TestCopyLocalCopiesFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	a := &App{}
	if err := a.CopyLocal(src, filepath.Join(dir, "y.txt")); err != nil {
		t.Fatalf("CopyLocal: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "y.txt")); string(b) != "hi" {
		t.Fatalf("copy failed: %q", b)
	}
}

func TestStatPathsReportsTypeAndName(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(file, []byte("12345"), 0o644)
	a := &App{}
	got := a.StatPaths([]string{file, dir, filepath.Join(dir, "nope")})
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name != "a.txt" || got[0].IsDir || got[0].Size != 5 {
		t.Fatalf("file info = %+v", got[0])
	}
	if !got[1].IsDir {
		t.Fatalf("dir info = %+v", got[1])
	}
	if got[2].Err == "" {
		t.Fatalf("missing path must carry Err: %+v", got[2])
	}
}

// 符号链接必须如实报错：localfs.Copy 对符号链接是静默跳过，若 StatPaths 不置 Err，
// 前端（handleSystemDrop 的 `if (i.err)` 分支）会把它记成「完成」却什么都没复制。
// 这里同时锁定「第二道防线」（localfs.Copy 仍然静默跳过）不被误改。
func TestStatPathsReportsSymlinkAsError(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("无法创建符号链接（环境不支持）: %v", err)
	}
	a := &App{}
	got := a.StatPaths([]string{link, real})
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Err == "" {
		t.Fatalf("符号链接必须带 Err，否则拖入会被记成「完成」却什么都没复制: %+v", got[0])
	}
	if !strings.Contains(got[0].Err, "符号链接") {
		t.Fatalf("Err 应说明符号链接, got %q", got[0].Err)
	}
	if got[0].IsDir || got[0].Size != 0 {
		t.Fatalf("符号链接不应被当成目录/带大小: %+v", got[0])
	}
	if got[1].Err != "" {
		t.Fatalf("普通文件不应带 Err: %+v", got[1])
	}
}

func TestSearchDepthNormalization(t *testing.T) {
	if got := normDepth(0); got != 5 {
		t.Fatalf("0 must normalize to 5, got %d", got)
	}
	if got := normDepth(-1); got != -1 {
		t.Fatalf("-1 means unlimited, got %d", got)
	}
	if got := normDepth(3); got != 3 {
		t.Fatalf("explicit depth must pass through, got %d", got)
	}
}

func TestAddAndRemoveBookmark(t *testing.T) {
	dir := t.TempDir()
	a := NewApp()
	a.cfgPath = filepath.Join(dir, "sshore.toml")
	a.cfg = config.DefaultAppConfig()
	b := config.Bookmark{Name: "日志", Scope: "remote", Host: "prod", Path: "/var/log"}
	if err := a.AddBookmark(b); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if got := a.ListLocations().Bookmarks; len(got) != 1 || got[0].Path != "/var/log" {
		t.Fatalf("bookmarks = %+v", got)
	}
	if err := a.RemoveBookmark("remote", "prod", "/var/log"); err != nil {
		t.Fatalf("RemoveBookmark: %v", err)
	}
	if got := a.ListLocations().Bookmarks; len(got) != 0 {
		t.Fatalf("bookmark not removed: %+v", got)
	}
}

// F1：队首已是同一项时必须短路——不刷新 TS、不重复写盘（与前端 store 的短路同构）。
// 用"手工设置的旧 TS"做断言，避免 RFC3339 秒级精度导致假绿。
func TestAddRecentHeadShortCircuit(t *testing.T) {
	a := newTestApp(t)
	a.cfg.LocalRecent = []config.RecentLocal{{Path: "/l", TS: "2000-01-01T00:00:00Z"}}
	a.cfg.RemoteRecent = []config.RecentRemote{{Host: "prod", Path: "/r", TS: "2000-01-01T00:00:00Z"}}

	if err := a.AddLocalRecent("/l"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddRemoteRecent("prod", "/r"); err != nil {
		t.Fatal(err)
	}
	if got := a.cfg.LocalRecent[0].TS; got != "2000-01-01T00:00:00Z" {
		t.Fatalf("本地队首同项必须短路（TS 不应刷新），got %q", got)
	}
	if got := a.cfg.RemoteRecent[0].TS; got != "2000-01-01T00:00:00Z" {
		t.Fatalf("远程队首同项必须短路（TS 不应刷新），got %q", got)
	}
	if len(a.cfg.LocalRecent) != 1 || len(a.cfg.RemoteRecent) != 1 {
		t.Fatalf("短路不得产生重复项: %+v / %+v", a.cfg.LocalRecent, a.cfg.RemoteRecent)
	}

	// 非队首项的重复记录仍要照常去重、置顶并刷新 TS。
	if err := a.AddRemoteRecent("db", "/srv"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddRemoteRecent("prod", "/r"); err != nil {
		t.Fatal(err)
	}
	if len(a.cfg.RemoteRecent) != 2 || a.cfg.RemoteRecent[0].Host != "prod" {
		t.Fatalf("重复记录非队首项必须置顶: %+v", a.cfg.RemoteRecent)
	}
	if a.cfg.RemoteRecent[0].TS == "2000-01-01T00:00:00Z" {
		t.Fatal("非队首项的重复记录应刷新 TS")
	}
}

func TestAddRemoteRecentDedupesAndCaps(t *testing.T) {
	dir := t.TempDir()
	a := NewApp()
	a.cfgPath = filepath.Join(dir, "sshore.toml")
	a.cfg = config.DefaultAppConfig()
	for i := 0; i < 25; i++ {
		if err := a.AddRemoteRecent("prod", "/p"+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	got := a.ListLocations().RemoteRecents
	if len(got) > 20 {
		t.Fatalf("must cap at 20, got %d", len(got))
	}
}
