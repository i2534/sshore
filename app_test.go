package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sftp"
)

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
	if info.Name != "sshore" {
		t.Fatalf("app name: %q", info.Name)
	}
	if info.Version == "" || info.Repo == "" {
		t.Fatalf("version/repo should be non-empty: %+v", info)
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

// M7: SftpGet 成功后记录 (host, dir(remote), dir(local))，且持久化落盘。
func TestSftpGetRecordsRecent(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpGet("prod-db", "alice", "/var/log/app.log", "/tmp/dl/app.log"); err != nil {
		t.Fatalf("get: %v", err)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 1 {
		t.Fatalf("want 1 recent entry, got %d: %+v", len(cfg.RecentSFTP), cfg.RecentSFTP)
	}
	e := cfg.RecentSFTP[0]
	if e.Host != "prod-db" || e.RemoteDir != "/var/log" || e.LocalDir != "/tmp/dl" {
		t.Fatalf("wrong entry: %+v", e)
	}
	if _, perr := time.Parse(time.RFC3339, e.TS); perr != nil || e.TS == "" {
		t.Fatalf("TS must be RFC3339, got %q", e.TS)
	}
}

// M7: SftpPut 成功后同样记录（remote/local 目录与 Get 对称）。
func TestSftpPutRecordsRecent(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	if err := a.SftpPut("prod-db", "alice", "/tmp/dl/app.log", "/var/log/app.log"); err != nil {
		t.Fatalf("put: %v", err)
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 1 {
		t.Fatalf("want 1 recent entry, got %d: %+v", len(cfg.RecentSFTP), cfg.RecentSFTP)
	}
	e := cfg.RecentSFTP[0]
	if e.Host != "prod-db" || e.RemoteDir != "/var/log" || e.LocalDir != "/tmp/dl" {
		t.Fatalf("wrong entry: %+v", e)
	}
}

// M7: SftpHome 成功后记录 (host, home, "")。
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
	if len(cfg.RecentSFTP) != 1 {
		t.Fatalf("want 1 recent entry, got %d: %+v", len(cfg.RecentSFTP), cfg.RecentSFTP)
	}
	e := cfg.RecentSFTP[0]
	if e.Host != "prod-db" || e.RemoteDir != "/home/alice" || e.LocalDir != "" {
		t.Fatalf("wrong entry: %+v", e)
	}
}

// M7: 重复记录同一 (host, remoteDir, localDir) 时旧条目被移除、新条目置顶，
// 不产生重复项。
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
	if len(cfg.RecentSFTP) != 2 {
		t.Fatalf("want 2 entries (dedup), got %d: %+v", len(cfg.RecentSFTP), cfg.RecentSFTP)
	}
	if cfg.RecentSFTP[0].Host != "h1" || cfg.RecentSFTP[1].Host != "h2" {
		t.Fatalf("re-recorded entry must move to front: %+v", cfg.RecentSFTP)
	}
}

// M7: 最近使用列表上限 10 条，最旧的被挤出。
func TestRecordRecentSFTPCapsAtTen(t *testing.T) {
	a := appWithFakeSFTP(t, "")
	for i := 0; i < 12; i++ {
		host := "host" + string(rune('a'+i))
		if err := a.SftpGet(host, "", "/r"+string(rune('0'+i)), "/local"); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadConfig(a.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecentSFTP) != 10 {
		t.Fatalf("want cap 10, got %d: %+v", len(cfg.RecentSFTP), cfg.RecentSFTP)
	}
	if cfg.RecentSFTP[0].Host != "hostl" || cfg.RecentSFTP[9].Host != "hostc" {
		t.Fatalf("newest-first order broken: %+v", cfg.RecentSFTP)
	}
}

// M7: ListRecentSFTP 契约——最新在前；无任何记录时返回空切片而非 nil。
func TestListRecentSFTPNewestFirstAndEmptyNotNil(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	if got := a.ListRecentSFTP(); got == nil || len(got) != 0 {
		t.Fatalf("no-data must return empty slice (not nil), got %#v", got)
	}
	a2 := NewApp()
	a2.cfg = &config.AppConfig{}
	if got := a2.ListRecentSFTP(); got == nil || len(got) != 0 {
		t.Fatalf("empty config must return empty slice (not nil), got %#v", got)
	}

	a3 := appWithFakeSFTP(t, "")
	for _, host := range []string{"h1", "h2", "h3"} {
		if err := a3.SftpGet(host, "", "/r/"+host, "/l/"+host); err != nil {
			t.Fatal(err)
		}
	}
	got := a3.ListRecentSFTP()
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(got), got)
	}
	want := []string{"h3", "h2", "h1"}
	for i, h := range want {
		if got[i].Host != h {
			t.Fatalf("index %d: want %s got %+v", i, h, got[i])
		}
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
