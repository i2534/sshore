package config

import (
	"path/filepath"
	"testing"
)

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshkit.toml")
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
