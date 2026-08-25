package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type AppSettings struct {
	AutoReconnectDefault bool `toml:"auto_reconnect_default" json:"auto_reconnect_default"`
}

type Tunnel struct {
	ID            string `toml:"id" json:"id"`
	Name          string `toml:"name" json:"name"`
	Mode          string `toml:"mode" json:"mode"` // local|remote|dynamic
	Host          string `toml:"host" json:"host"`
	User          string `toml:"user,omitempty" json:"user,omitempty"`
	Port          int    `toml:"port,omitempty" json:"port,omitempty"`
	ListenBind    string `toml:"listen_bind" json:"listen_bind"`
	ListenPort    int    `toml:"listen_port" json:"listen_port"`
	TargetHost    string `toml:"target_host" json:"target_host"`
	TargetPort    int    `toml:"target_port" json:"target_port"`
	ProxyJump     string `toml:"proxy_jump,omitempty" json:"proxy_jump,omitempty"`
	AutoReconnect bool   `toml:"auto_reconnect" json:"auto_reconnect"`
	Enabled       bool   `toml:"enabled" json:"enabled"`
}

type RecentSFTP struct {
	Host      string `toml:"host" json:"host"`
	RemoteDir string `toml:"remote_dir" json:"remote_dir"`
	LocalDir  string `toml:"local_dir" json:"local_dir"`
	TS        string `toml:"ts" json:"ts"`
}

type AppConfig struct {
	App        AppSettings  `toml:"app" json:"app"`
	Tunnels    []Tunnel     `toml:"tunnels" json:"tunnels"`
	RecentSFTP []RecentSFTP `toml:"recent_sftp" json:"recent_sftp"`
}

// LoadConfig reads the TOML config; returns safe defaults if the file doesn't exist.
func LoadConfig(path string) (*AppConfig, error) {
	cfg := &AppConfig{App: AppSettings{AutoReconnectDefault: true}}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes the config to path with 0600 perms, creating parents.
func SaveConfig(path string, cfg *AppConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return err
	}
	return f.Sync()
}

// DefaultConfigPath returns the per-OS config path.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sshkit", "sshkit.toml"), nil
}

// NewTunnelID returns a random 16-byte hex id.
func NewTunnelID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
