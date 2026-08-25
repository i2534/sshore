package main

import (
	"os"
	"path/filepath"
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
