package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/importer"
	"sshore/internal/osutil"
	"sshore/internal/sftp"
)

type App struct {
	ctx     context.Context
	forward *forward.Ctrl
	sftp    *sftp.Ctrl
	cfg     *config.AppConfig
	cfgPath string
	// H2: startup 早于 Init 注入 emit，加载错误先记录于此，Init 时补发事件
	emit       func(forward.Event)
	cfgLoadErr error
}

func NewApp() *App {
	return &App{}
}

// loadOrBackupConfig loads the config; on parse failure it first backs up the
// original file to <path>.bak-<unix-ts> (byte-for-byte), then returns a usable
// empty config plus the error, so a later saveConfig can never overwrite the
// user's corrupt-but-recoverable file without a backup in place (H2).
func (a *App) loadOrBackupConfig(path string) (*config.AppConfig, error) {
	cfg, err := config.LoadConfig(path)
	if err == nil {
		return cfg, nil
	}
	if data, rerr := os.ReadFile(path); rerr == nil {
		bak := fmt.Sprintf("%s.bak-%d", path, time.Now().Unix())
		werr := os.WriteFile(bak, data, 0600)
		if werr == nil {
			return config.DefaultAppConfig(), fmt.Errorf("配置文件 %s 解析失败，已备份到 %s，本次以空配置启动: %w", path, bak, err)
		}
		return config.DefaultAppConfig(), fmt.Errorf("配置文件 %s 解析失败且备份失败（%v），本次以空配置启动: %w", path, werr, err)
	}
	// 文件不存在或不可读：没有内容可备份
	return config.DefaultAppConfig(), err
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	p, _ := config.DefaultConfigPath()
	a.cfgPath = p
	cfg, err := a.loadOrBackupConfig(p)
	a.cfg = cfg
	if err != nil {
		a.cfgLoadErr = err
	}
}

// Init wires controllers. emit forwards subsystem events to the frontend.
// forward needs a Spawner (long-lived ssh -N); sftp needs a blocking Runner (one-shot ops).
func (a *App) Init(emit func(forward.Event)) {
	a.emit = emit
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit, nil)
	a.sftp = sftp.NewCtrl(osutil.NewRunner(), emit)
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	// H2: startup 阶段发现的配置损坏延迟到这里发事件（此时 emit 已可用）。
	// 注意：前端订阅 log 事件可能更晚，该事件属尽力而为；可靠的用户可见保障
	// 是 .bak 备份文件 + 空配置，错误详情同时保留在 cfgLoadErr 中。
	if a.cfgLoadErr != nil && a.emit != nil {
		a.emit(forward.Event{
			SourceType: "system",
			SourceID:   "app",
			TS:         time.Now().Format(time.RFC3339),
			Level:      "error",
			Message:    a.cfgLoadErr.Error(),
		})
	}
}

func (a *App) ListHosts() []string {
	path, err := config.FindSSHConfigPath()
	if err != nil {
		return []string{}
	}
	hosts, err := config.EnumerateHosts(path)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Alias)
	}
	return out
}

// ListHostsDetailed 返回经 ssh -G 权威富化（hostname/user/port/proxyjump）的
// 主机列表。单个 alias 的 -G 失败/超时静默回退库值；≤8 并发、单次 ≤5s。
func (a *App) ListHostsDetailed() []config.Host {
	path, err := config.FindSSHConfigPath()
	if err != nil {
		return []config.Host{}
	}
	hosts, err := config.EnumerateHostsDetailed(path, nil)
	if err != nil {
		return []config.Host{}
	}
	return hosts
}

func (a *App) ListTunnels() []config.Tunnel {
	if a.cfg == nil {
		return []config.Tunnel{}
	}
	return a.cfg.Tunnels
}

// TunnelStates 返回各隧道运行态（id → state 字符串），供前端四态圆点渲染。
func (a *App) TunnelStates() map[string]string {
	return a.forward.States()
}

func (a *App) CreateTunnel(t config.Tunnel) error {
	if err := forward.ValidateTunnel(t); err != nil {
		return err
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	if t.ID == "" {
		t.ID = config.NewTunnelID()
	}
	// M7: 接线远端转发端口冲突检查——(host, bind, port) 已被其他规则占用即拒绝，
	// 与 mode 无关（spec §4.5）。
	if err := forward.CheckRemoteConflict(a.cfg.Tunnels, t); err != nil {
		return err
	}
	a.cfg.Tunnels = append(a.cfg.Tunnels, t)
	return a.saveConfig()
}

// UpdateTunnel replaces an existing tunnel (matched by ID) with updated fields.
// If the tunnel is running, it is stopped first because the config changed.
func (a *App) UpdateTunnel(t config.Tunnel) error {
	if err := forward.ValidateTunnel(t); err != nil {
		return err
	}
	idx := -1
	for i, e := range a.cfg.Tunnels {
		if e.ID == t.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("tunnel not found")
	}
	// M7: 改动前查重——CheckRemoteConflict 跳过 e.ID==t.ID，保留自身键可放行。
	if err := forward.CheckRemoteConflict(a.cfg.Tunnels, t); err != nil {
		return err
	}
	if a.cfg.Tunnels[idx].Enabled {
		_ = a.forward.Stop(t.ID)
		t.Enabled = false
	}
	a.cfg.Tunnels[idx] = t
	return a.saveConfig()
}

// DeleteTunnel removes a tunnel (matched by ID), stopping it if running.
func (a *App) DeleteTunnel(id string) error {
	idx := -1
	for i, e := range a.cfg.Tunnels {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("tunnel not found")
	}
	if a.cfg.Tunnels[idx].Enabled {
		_ = a.forward.Stop(id)
	}
	a.cfg.Tunnels = append(a.cfg.Tunnels[:idx], a.cfg.Tunnels[idx+1:]...)
	return a.saveConfig()
}

func (a *App) ImportCommand(cmd string) ([]config.Tunnel, error) {
	tunnels, err := importer.Parse(cmd)
	if err != nil {
		return nil, err
	}
	for _, t := range tunnels {
		_ = a.CreateTunnel(t)
	}
	return tunnels, nil
}

func (a *App) StartTunnel(id string) error {
	t, ok := a.findTunnel(id)
	if !ok {
		return errors.New("tunnel not found")
	}
	if err := a.forward.Start(t); err != nil {
		return err
	}
	t.Enabled = true
	a.updateTunnel(t)
	return a.saveConfig()
}

func (a *App) StopTunnel(id string) error {
	if err := a.forward.Stop(id); err != nil {
		return err
	}
	if t, ok := a.findTunnel(id); ok {
		t.Enabled = false
		a.updateTunnel(t)
		return a.saveConfig()
	}
	return nil
}

func (a *App) saveConfig() error {
	if a.cfgPath == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			return err
		}
		a.cfgPath = p
	}
	return config.SaveConfig(a.cfgPath, a.cfg)
}

func (a *App) updateTunnel(u config.Tunnel) {
	for i := range a.cfg.Tunnels {
		if a.cfg.Tunnels[i].ID == u.ID {
			a.cfg.Tunnels[i] = u
		}
	}
}

// AutoStartEnabled starts all tunnels that were enabled at last run.
// M10: 单个隧道失败不得中止循环——逐项收集失败、经事件管道上报，最后汇总返回。
func (a *App) AutoStartEnabled() error {
	var errs []error
	for _, t := range a.cfg.Tunnels {
		if !t.Enabled {
			continue
		}
		if err := a.forward.Start(t); err != nil {
			msg := fmt.Sprintf("自动启动隧道 %s 失败: %v", t.ID, err)
			if a.emit != nil {
				a.emit(forward.Event{
					SourceType: "tunnel",
					SourceID:   t.ID,
					TS:         time.Now().Format(time.RFC3339),
					Level:      "error",
					Message:    msg,
				})
			}
			errs = append(errs, errors.New(msg))
		}
	}
	return errors.Join(errs...)
}

func (a *App) findTunnel(id string) (config.Tunnel, bool) {
	if a.cfg == nil {
		return config.Tunnel{}, false
	}
	for _, t := range a.cfg.Tunnels {
		if t.ID == id {
			return t, true
		}
	}
	return config.Tunnel{}, false
}

func (a *App) SftpList(host, user, path string) ([]sftp.Item, error) {
	return a.sftp.List(host, user, path)
}
func (a *App) SftpGet(host, user, remote, local string) error {
	if err := a.sftp.Get(host, user, remote, local); err != nil {
		return err
	}
	a.recordRecentSFTP(host, filepath.Dir(remote), filepath.Dir(local))
	return nil
}

// SftpGetDir recursively downloads a remote directory tree (`sftp get -r`).
func (a *App) SftpGetDir(host, user, remote, local string) error {
	if err := a.sftp.GetRecursive(host, user, remote, local); err != nil {
		return err
	}
	a.recordRecentSFTP(host, filepath.Dir(remote), filepath.Dir(local))
	return nil
}
func (a *App) SftpPut(host, user, local, remote string) error {
	if err := a.sftp.Put(host, user, local, remote); err != nil {
		return err
	}
	a.recordRecentSFTP(host, filepath.Dir(remote), filepath.Dir(local))
	return nil
}
func (a *App) SftpRemove(host, user, path string) error {
	return a.sftp.Remove(host, user, path)
}
func (a *App) SftpMkdir(host, user, path string) error {
	return a.sftp.Mkdir(host, user, path)
}
func (a *App) SftpRename(host, user, oldPath, newPath string) error {
	return a.sftp.Rename(host, user, oldPath, newPath)
}
func (a *App) SftpConnect(host string) error {
	return a.sftp.Connect(host, "")
}
func (a *App) SftpDisconnect(host string) error {
	return a.sftp.Disconnect(host)
}
func (a *App) SftpConnected(host string) bool {
	return a.sftp.Connected(host)
}

// DeleteLocal removes a local file or directory (recursively for dirs).
func (a *App) DeleteLocal(path string) error {
	p, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkDeletablePath(p); err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// checkDeletablePath is the M9 defense-in-depth gate: these paths are never
// handed to os.RemoveAll — the Unix filesystem root, Windows volume roots
// (e.g. "C:\"), and the user's home directory itself (exact match only).
func checkDeletablePath(p string) error {
	if p == string(filepath.Separator) {
		return fmt.Errorf("拒绝删除文件系统根目录: %s", p)
	}
	if isVolumeRoot(p) {
		return fmt.Errorf("拒绝删除磁盘根目录: %s", p)
	}
	if home, err := os.UserHomeDir(); err == nil && p == filepath.Clean(home) {
		return fmt.Errorf("拒绝删除用户主目录: %s", p)
	}
	return nil
}

// isVolumeRoot reports whether p is a Windows volume root such as "C:\" or "C:".
func isVolumeRoot(p string) bool {
	return (len(p) == 2 && p[1] == ':') ||
		(len(p) == 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

// MkdirLocal creates a local directory (and parents).
func (a *App) MkdirLocal(path string) error {
	return os.MkdirAll(path, 0755)
}

// RenameLocal renames/moves a local file or directory.
func (a *App) RenameLocal(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// StatLocal returns the size in bytes of a local file (0 for dirs/missing).
func (a *App) StatLocal(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, nil
	}
	return info.Size(), nil
}

// PickLocalFile opens a native file picker and returns the chosen local path
// ("" if cancelled). Used as the upload source for SftpPut.
func (a *App) PickLocalFile() (string, error) {
	if a.ctx == nil {
		return "", errors.New("no context")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择要上传的文件",
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// ListLocal reads a local directory and returns its entries (dirs + files).
// It reuses sftp.Item for a uniform frontend shape.
func (a *App) ListLocal(path string) ([]sftp.Item, error) {
	if path == "" {
		path = "."
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var items []sftp.Item
	for _, e := range entries {
		info, _ := e.Info()
		item := sftp.Item{Name: e.Name(), IsDir: e.IsDir()}
		if info != nil {
			item.Size = info.Size()
			item.ModTime = info.ModTime().Format("2006-01-02 15:04")
			item.Mode = info.Mode().String()
		}
		items = append(items, item)
	}
	return items, nil
}

// PickLocalDir opens a native directory picker and returns the chosen local
// directory ("" if cancelled). Used as the download destination.
func (a *App) PickLocalDir() (string, error) {
	if a.ctx == nil {
		return "", errors.New("no context")
	}
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择下载保存目录",
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// HomeDir returns the current user's home directory.
func (a *App) HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

// Cwd returns the process's current working directory (used as the initial
// local pane path).
func (a *App) Cwd() (string, error) {
	return os.Getwd()
}

// SftpHome returns the remote user's home directory as an absolute path, by
// running `pwd` over a fresh, reused sftp connection. Used as the initial
// remote pane path.
func (a *App) SftpHome(host string) (string, error) {
	home, err := a.sftp.Home(host, "")
	if err != nil {
		return "", err
	}
	a.recordRecentSFTP(host, home, "")
	return home, nil
}

// recordRecentSFTP 记录一次成功的 SFTP 操作到最近使用列表：
// 相同 (host, remoteDir, localDir) 的旧条目移除后新条目置顶，上限 10 条，
// 最新在前；落盘沿用 saveConfig 的 fire-and-forget 模式（见 OnShutdown）。
func (a *App) recordRecentSFTP(host, remoteDir, localDir string) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	rec := config.RecentSFTP{
		Host:      host,
		RemoteDir: remoteDir,
		LocalDir:  localDir,
		TS:        time.Now().Format(time.RFC3339),
	}
	kept := a.cfg.RecentSFTP[:0]
	for _, e := range a.cfg.RecentSFTP {
		if e.Host == host && e.RemoteDir == remoteDir && e.LocalDir == localDir {
			continue // 去重：旧条目丢弃，由新条目顶替并置顶
		}
		kept = append(kept, e)
	}
	a.cfg.RecentSFTP = append([]config.RecentSFTP{rec}, kept...)
	if len(a.cfg.RecentSFTP) > 10 {
		a.cfg.RecentSFTP = a.cfg.RecentSFTP[:10]
	}
	_ = a.saveConfig()
}

// ListRecentSFTP 返回 SFTP 最近使用列表（最新在前，最多 10 条）；
// 无数据时返回空切片而非 nil（前端契约）。
func (a *App) ListRecentSFTP() []config.RecentSFTP {
	if a.cfg == nil {
		return []config.RecentSFTP{}
	}
	if a.cfg.RecentSFTP == nil {
		return []config.RecentSFTP{}
	}
	return a.cfg.RecentSFTP
}

func (a *App) OnShutdown() {
	a.forward.OnShutdown()
	a.sftp.CloseAll()
	_ = a.saveConfig()
}
