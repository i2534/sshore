package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/BurntSushi/toml"
)

type AppSettings struct {
	AutoReconnectDefault bool    `toml:"auto_reconnect_default" json:"auto_reconnect_default"`
	Theme                string  `toml:"theme" json:"theme"`                               // dark|light|system
	FontScale            float64 `toml:"font_scale" json:"font_scale"`                     // 字号缩放系数
	LatinFont            string  `toml:"latin_font,omitempty" json:"latin_font,omitempty"` // 英文字体（空=系统默认）
	CJKFont              string  `toml:"cjk_font,omitempty" json:"cjk_font,omitempty"`     // 中文字体（空=系统默认）
	AutoStartOnLaunch    bool    `toml:"auto_start_on_launch" json:"auto_start_on_launch"` // 启动后自动连接转发通道
}

// Normalize 兜底无效设置：主题缺省为跟随系统、字号系数非法时回退到 1，
// 并把字号系数限制在安全区间，避免前端应用出 0/负字号导致文字不可见。
func (s *AppSettings) Normalize() {
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.FontScale <= 0 {
		s.FontScale = 1
	}
	if s.FontScale < 0.5 {
		s.FontScale = 0.5
	}
	if s.FontScale > 2 {
		s.FontScale = 2
	}
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

// normalize applies per-field defaults to the whole config (currently just App).
func (c *AppConfig) normalize() { c.App.Normalize() }

var tmpSeq int64

// DefaultAppConfig returns a fresh config with safe defaults.
func DefaultAppConfig() *AppConfig {
	c := &AppConfig{
		App: AppSettings{
			AutoReconnectDefault: true,
			Theme:                "system",
			FontScale:            1,
			AutoStartOnLaunch:    true,
		},
	}
	c.normalize()
	return c
}

// LoadConfig reads the TOML config; returns safe defaults if the file doesn't exist.
func LoadConfig(path string) (*AppConfig, error) {
	cfg := DefaultAppConfig()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	// 旧配置缺少新字段时，decode 只覆盖文件中出现的键，预先填充的默认值保留；
	// 显式写入的空/非法值在此统一兜底，保证读到的设置总是可用的。
	cfg.normalize()
	return cfg, nil
}

// SaveConfig writes the config to path with 0600 perms, creating parents.
// M11: 原子写——先写 <path>.tmp-<pid> 再 rename 覆盖，读者任何时刻只能看到
// 完整的旧文件或完整的新文件，并发保存不会撕裂目标文件。
func SaveConfig(path string, cfg *AppConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// 同一进程内多个 goroutine 共享 PID，需要序列号保证 tmp 名唯一
	tmp := fmt.Sprintf("%s.tmp-%d-%d", path, os.Getpid(), atomic.AddInt64(&tmpSeq, 1))
	if err := writeConfigFile(tmp, cfg); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // rename 失败也不遗留临时文件
		return err
	}
	return nil
}

// writeConfigFile encodes cfg as TOML into a fresh file at path with mode 0600.
func writeConfigFile(path string, cfg *AppConfig) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// DefaultConfigPath returns the per-OS config path.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sshore", "sshore.toml"), nil
}

// NewTunnelID returns a random 16-byte hex id.
func NewTunnelID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
