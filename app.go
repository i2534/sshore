package main

import (
	"context"
	"errors"

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
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	p, _ := config.DefaultConfigPath()
	a.cfgPath = p
	cfg, err := config.LoadConfig(p)
	if err == nil {
		a.cfg = cfg
	}
}

// Init wires controllers. emit forwards subsystem events to the frontend.
// forward needs a Spawner (long-lived ssh -N); sftp needs a blocking Runner (one-shot ops).
func (a *App) Init(emit func(forward.Event)) {
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit)
	a.sftp = sftp.NewCtrl(osutil.NewRunner(), emit)
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
}

func (a *App) ListHosts() []string {
	path, err := config.FindSSHConfigPath()
	if err != nil {
		return nil
	}
	hosts, err := config.EnumerateHosts(path)
	if err != nil {
		return nil
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Alias
	}
	return out
}

func (a *App) ListTunnels() []config.Tunnel {
	if a.cfg == nil {
		return nil
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

func (a *App) OnShutdown() {
	a.forward.OnShutdown()
	_ = a.saveConfig()
}
