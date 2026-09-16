package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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

// Bookmark 是一个手动固定的位置；scope=remote 时 host 有意义。
type Bookmark struct {
	Name  string `toml:"name" json:"name"`
	Scope string `toml:"scope" json:"scope"`
	Host  string `toml:"host" json:"host"`
	Path  string `toml:"path" json:"path"`
}

type RecentLocal struct {
	Path string `toml:"path" json:"path"`
	TS   string `toml:"ts" json:"ts"`
}

type RecentRemote struct {
	Host string `toml:"host" json:"host"`
	Path string `toml:"path" json:"path"`
	TS   string `toml:"ts" json:"ts"`
}

// SyncRule 是一条"监控远端路径并同步到本地"的规则。
type SyncRule struct {
	ID            string   `toml:"id" json:"id"`
	Name          string   `toml:"name" json:"name"`
	Host          string   `toml:"host" json:"host"`
	User          string   `toml:"user,omitempty" json:"user,omitempty"`
	Kind          string   `toml:"kind" json:"kind"` // dir | file
	RemotePath    string   `toml:"remote_path" json:"remote_path"`
	LocalPath     string   `toml:"local_path" json:"local_path"` // 恒为目录
	MaxDepth      int      `toml:"max_depth" json:"max_depth"`   // 0=仅本层 N=递归N层 -1=无限
	Excludes      []string `toml:"excludes" json:"excludes"`
	MirrorDelete  bool     `toml:"mirror_delete" json:"mirror_delete"`
	ForcePoll     bool     `toml:"force_poll" json:"force_poll"` // 反向字段：零值即"优先 inotify"
	PollIntervalS int      `toml:"poll_interval_s" json:"poll_interval_s"`
	AutoReconnect *bool    `toml:"auto_reconnect,omitempty" json:"auto_reconnect"`
	Enabled       bool     `toml:"enabled" json:"enabled"`
}

// DefaultExcludes 是远端路径的默认忽略集合（过滤的是**远端**路径）。
// 不要放 *.part：那是我们本地临时文件的后缀，远端不会出现。
func DefaultExcludes() []string {
	return []string{".git/", "node_modules/", "*.swp", "*~", ".DS_Store"}
}

// Reconnect 返回"意外断开时是否自动重连"。缺键（nil）按 true 处理——真正的
// 默认值由 AppConfig.normalize() 从全局 App.AutoReconnectDefault 灌入，
// 这里只是防止绕过 LoadConfig 直接构造 SyncRule 时解引用 nil。
func (r *SyncRule) Reconnect() bool {
	return r.AutoReconnect == nil || *r.AutoReconnect
}

// Normalize 兜底零值。手改配置缺键时，零值不得静默改变行为
// （例如 poll_interval_s=0 或 excludes=nil）。
func (r *SyncRule) Normalize() {
	if r.Kind != "file" {
		r.Kind = "dir"
	}
	if r.MaxDepth < -1 {
		r.MaxDepth = -1
	}
	if r.PollIntervalS < 1 {
		r.PollIntervalS = 5
	}
	if r.PollIntervalS > 3600 {
		r.PollIntervalS = 3600
	}
	if r.Excludes == nil {
		r.Excludes = DefaultExcludes()
	}
}

// NewSyncID 与 NewTunnelID 同构：32 位纯十六进制、无前缀。
func NewSyncID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type AppConfig struct {
	App        AppSettings  `toml:"app" json:"app"`
	Tunnels    []Tunnel     `toml:"tunnels" json:"tunnels"`
	RecentSFTP []RecentSFTP `toml:"recent_sftp" json:"recent_sftp"`
	Syncs      []SyncRule   `toml:"syncs" json:"syncs"`

	Bookmarks      []Bookmark     `toml:"bookmarks" json:"bookmarks"`
	LocalRecent    []RecentLocal  `toml:"local_recent" json:"local_recent"`
	RemoteRecent   []RecentRemote `toml:"remote_recent" json:"remote_recent"`
	LegacyMigrated bool           `toml:"legacy_migrated" json:"legacy_migrated"`
}

// normalize applies per-field defaults to the whole config (currently just App).
func (c *AppConfig) normalize() {
	c.App.Normalize()
	for i := range c.Syncs {
		c.Syncs[i].Normalize()
		// spec §5.2：auto_reconnect 缺键时取全局默认（默认 true）。
		// 用例：手写配置只写 host/remote_path/local_path，不应被静默关闭重连。
		if c.Syncs[i].AutoReconnect == nil {
			v := c.App.AutoReconnectDefault
			c.Syncs[i].AutoReconnect = &v
		}
	}
}

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

const maxRecent = 20

// MigrateLegacyRecents 把旧 recent_sftp（{host, remote_dir, local_dir, ts}）拆成
// 双侧最近位置。用显式标记 LegacyMigrated 判定，**不用**「新字段为空」——否则用户
// 清空书签/最近后旧数据会被复活。幂等；迁移后由调用方 saveConfig 落盘。
func MigrateLegacyRecents(cfg *AppConfig) bool {
	if cfg == nil || cfg.LegacyMigrated {
		return false
	}
	cfg.LegacyMigrated = true
	if len(cfg.RecentSFTP) == 0 {
		return true
	}
	sort.SliceStable(cfg.RecentSFTP, func(i, j int) bool { return cfg.RecentSFTP[i].TS > cfg.RecentSFTP[j].TS })
	seenR := map[string]bool{}
	seenL := map[string]bool{}
	for _, r := range cfg.RecentSFTP {
		if r.Host != "" && r.RemoteDir != "" {
			k := r.Host + "\x00" + r.RemoteDir
			if !seenR[k] && len(cfg.RemoteRecent) < maxRecent {
				seenR[k] = true
				cfg.RemoteRecent = append(cfg.RemoteRecent, RecentRemote{Host: r.Host, Path: r.RemoteDir, TS: r.TS})
			}
		}
		if r.LocalDir != "" {
			if !seenL[r.LocalDir] && len(cfg.LocalRecent) < maxRecent {
				seenL[r.LocalDir] = true
				cfg.LocalRecent = append(cfg.LocalRecent, RecentLocal{Path: r.LocalDir, TS: r.TS})
			}
		}
	}
	// 迁移完成即清空旧字段：否则 SaveConfig 的全量编码会继续把 recent_sftp 写回磁盘
	// （spec §10.2「旧字段只读兼容、不再写回」）。
	cfg.RecentSFTP = nil
	return true
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
	if err := renameWithRetry(tmp, path); err != nil {
		_ = os.Remove(tmp) // rename 失败也不遗留临时文件
		return err
	}
	return nil
}

// renameWithRetry 在 Windows 上有必要：目标文件若正被其他句柄打开（杀毒/索引器/
// 并发读者），MoveFileEx 会返回共享冲突（Access is denied）。这类冲突通常是瞬时的，
// 短暂重试即可成功；超过次数仍失败才如实返回错误。Unix 上 rename 极少瞬时失败，
// 重试无副作用。
func renameWithRetry(oldpath, newpath string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = os.Rename(oldpath, newpath); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
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

// DefaultPresetsPath 返回预设文件路径：与主配置同目录（unix 是 ~/.config/sshore/presets.toml，
// Windows 是 %AppData%/sshore/presets.toml）。
//
// 单独一个文件是有意为之：预设是**用户手改**的，而主配置会被应用频繁整份重编码
// （任何书签/最近保存都走 SaveConfig），放一起会反复抹掉用户写的注释；且旧版本
// 二进制不认识这个文件，**降级不会丢预设**（放主配置里会被旧版保存时整段抹掉）。
func DefaultPresetsPath() (string, error) {
	cfg, err := DefaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfg), "presets.toml"), nil
}

// Preset 是预设文件里的一条预设；scope 缺省 local。
type Preset struct {
	Name  string `toml:"name" json:"name"`
	Scope string `toml:"scope,omitempty" json:"scope,omitempty"` // local | remote
	Host  string `toml:"host,omitempty" json:"host,omitempty"`   // 仅 scope=remote：留空 = 所有主机
	Path  string `toml:"path" json:"path"`
}

// NormalizeScope 归一 scope：大小写/首尾空白是手写配置的常见笔误，而预设层是精确比较，
// 不归一就等于"配了却什么都没有"。（store.go 需新增 import "strings"）
func NormalizeScope(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "local"
	}
	return s
}

// LoadPresets 读取预设文件。文件不存在返回 (nil, os.ErrNotExist)，调用方据此决定是否播种；
// 解析失败返回错误，并且**绝不改写用户文件**（调用方只记录 + 降级，不覆盖）。
func LoadPresets(path string) ([]Preset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Presets []Preset `toml:"presets"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, fmt.Errorf("解析预设文件 %s: %w", path, err)
	}
	for i := range doc.Presets {
		doc.Presets[i].Scope = NormalizeScope(doc.Presets[i].Scope)
	}
	return doc.Presets, nil
}

// SavePresets 以 0600 原子写入预设文件（先写 <path>.tmp-<pid>-<seq> 再 rename，与 SaveConfig
// 同一套原语）。**只在"首次生成"时被调用**，之后应用不再改写这个文件。
func SavePresets(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%d", path, os.Getpid(), atomic.AddInt64(&tmpSeq, 1))
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameWithRetry(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

const presetsHeader = "# SSHore 位置预设（「📍位置」下拉里的「预设」组 = 本文件的全部内容）\n" +
	"#\n" +
	"# 这个文件只在你首次启动时由应用生成一次，之后应用**不会**再改写它 ——\n" +
	"# 你可以放心加注释、调顺序、改名、删条目。\n" +
	"#   - 删掉哪条，下拉里就没有哪条；保留文件但删光条目 = 预设组消失（不会重建）\n" +
	"#   - 本地面板的 path 用绝对路径（~/xxx 会展开为主目录）；远端的 \"~\" 表示远端 home\n" +
	"#   - scope 缺省 local；remote 条目可用 host 限定主机（留空 = 所有主机）\n" +
	"#   - Windows 路径推荐用单引号字面量（双引号里反斜杠是转义符，写错会让文件解析失败）\n" +
	"#   - 改完重启应用生效\n\n"

// PresetsTemplate 渲染"首次生成"用的文件内容：说明注释 + 默认条目。
// 字段一律用 %q 写出，Windows 反斜杠会被正确转义，保证生成的文件一定能被解析。
func PresetsTemplate(seed []Preset) []byte {
	var b strings.Builder
	b.WriteString(presetsHeader)
	for _, p := range seed {
		b.WriteString("[[presets]]\n")
		fmt.Fprintf(&b, "name = %q\n", p.Name)
		fmt.Fprintf(&b, "scope = %q\n", NormalizeScope(p.Scope))
		if p.Host != "" {
			fmt.Fprintf(&b, "host = %q\n", p.Host)
		}
		fmt.Fprintf(&b, "path = %q\n\n", p.Path)
	}
	return []byte(b.String())
}
