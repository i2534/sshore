package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"sshkit/internal/config"
	"sshkit/internal/forward"
	"sshkit/internal/importer"
	"sshkit/internal/osutil"
	"sshkit/internal/sftp"
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
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit)
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

func (a *App) ListTunnels() []config.Tunnel {
	if a.cfg == nil {
		return []config.Tunnel{}
	}
	return a.cfg.Tunnels
}

func (a *App) CreateTunnel(t config.Tunnel) error {
	if !forward.ValidateHost(t.Host) {
		return errors.New("invalid host alias")
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	if t.ID == "" {
		t.ID = config.NewTunnelID()
	}
	a.cfg.Tunnels = append(a.cfg.Tunnels, t)
	return a.saveConfig()
}

// UpdateTunnel replaces an existing tunnel (matched by ID) with updated fields.
// If the tunnel is running, it is stopped first because the config changed.
func (a *App) UpdateTunnel(t config.Tunnel) error {
	if !forward.ValidateHost(t.Host) {
		return errors.New("invalid host alias")
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
func (a *App) AutoStartEnabled() error {
	for _, t := range a.cfg.Tunnels {
		if t.Enabled {
			if err := a.forward.Start(t); err != nil {
				return err
			}
		}
	}
	return nil
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
	return a.sftp.Get(host, user, remote, local)
}
func (a *App) SftpPut(host, user, local, remote string) error {
	return a.sftp.Put(host, user, local, remote)
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
	return os.RemoveAll(path)
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
	return a.sftp.Home(host, "")
}

func (a *App) OnShutdown() {
	a.forward.OnShutdown()
	a.sftp.CloseAll()
	_ = a.saveConfig()
}
