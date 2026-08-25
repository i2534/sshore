package main

import (
	"context"
	"errors"

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
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// Init wires controllers. emit forwards subsystem events to the frontend.
// forward needs a Spawner (long-lived ssh -N); sftp needs a blocking Runner (one-shot ops).
func (a *App) Init(emit func(forward.Event)) {
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit)
	a.sftp = sftp.NewCtrl(osutil.NewRunner(), emit)
	a.cfg = &config.AppConfig{}
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
	return nil
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
	return a.forward.Start(t)
}

func (a *App) StopTunnel(id string) error {
	return a.forward.Stop(id)
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
func (a *App) OnShutdown() {
	a.forward.OnShutdown()
}
