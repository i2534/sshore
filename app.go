package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	gosruntime "runtime"
	stdsync "sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/importer"
	"sshore/internal/localfs"
	"sshore/internal/osutil"
	"sshore/internal/preset"
	"sshore/internal/sftp"
	"sshore/internal/sync"
	"sshore/internal/update"
)

type App struct {
	ctx     context.Context
	forward *forward.Ctrl
	sftp    *sftp.Ctrl
	cfg     *config.AppConfig
	cfgPath string
	sync    *sync.Ctrl

	// cfgMu 保护 cfg.App 与 cfg.SftpUI。
	//
	// cfg.App：更新服务 goroutine（Config/Save 回调）读、Wails 调用线程
	// （GetSettings/SetSettings）写。setAppSettings 是**整结构赋值**，因此任何对
	// a.cfg.App 子字段的裸读都会与它构成数据竞争 —— 所有读点都必须经过受锁的
	// appSettings / autoStartOnLaunch / sftpTransport（Task 11 fix round 1；
	// 裸读 sftpTransport / AutoStartOnLaunch 由最终评审 I-3 收口）。
	//
	// cfg.SftpUI 与 locations 三段（Bookmarks/LocalRecent/RemoteRecent）的锁规约
	// （规格 §6.3.4 红线 3 / §6.3.5）：四个写入函数 AddBookmark / RemoveBookmark /
	// AddLocalRecent / AddRemoteRecent（含内部 recordRecentSFTP）一律
	//   ① 写锁内**只构造新值**（新 slice / 新 map 后整结构赋值），锁内绝不做 IO；
	//   ② 解锁后再 saveConfig()；changed == false 时直接 return nil（不写盘）。
	// 为什么必须构造新值：snapshotConfig() 是**浅拷贝**（`cp := *a.cfg`），slice 的
	// 底层数组与 map 的内容会被锁外的 TOML 编码共享 —— 原地复用 `x[:0]` /
	// `append(a.cfg.X, ...)` / 原地写 `LastRemoteDirs[host]` 都与该编码并发，
	// 是真的数据竞争（-race 可复现），裸 `&config.AppConfig{}` 上还是 nil map panic。
	//
	// 铁律：锁内只允许调 *Locked 系列私有 helper，**禁止调公开绑定**
	// （AddRemoteRecent 等）。cfgMu 是不可重入的 RWMutex，同锁重入即死锁 ——
	// 表现是测试 hang 到 timeout，而不是报错。
	cfgMu stdsync.RWMutex

	// H2: startup 早于 Init 注入 emit，加载错误先记录于此，Init 时补发事件
	emit       func(forward.Event)
	cfgLoadErr error

	// 深搜取消表：id → cancel。app.go 已 import "sshore/internal/sync"，
	// 标准库互斥锁必须用 stdsync 别名，否则与 sync.Ctrl 冲突。
	searchMu      stdsync.Mutex
	searchCancels map[string]context.CancelFunc

	// H2 同构：startup 阶段发现的预设文件问题（首次生成失败 / 坏文件），
	// Init 时 emit 补发，前端日志面板可见。
	presetsPath string
	presetsErr  error

	// updater 是更新检查/下载状态机。构造在 startup(ctx) 而不是 Init：
	// Init 没有 ctx，且 app_test 直接调用 Init 的路径很多，在那里起后台 goroutine
	// 会污染测试且无人 Shutdown（Task 11 裁定 1）。
	updater *update.Service
}

var (
	// Version/Repo 由构建注入（-X main.Version=...），未注入时用默认值。
	Version = "dev"
	Repo    = "https://github.com/i2534/sshore"
)

// appTitle 返回主窗口标题："SSHore <版本>"。版本取自构建注入的 Version
// （未注入时为 dev），使发布包与开发态窗口标题都带版本号（见 Makefile LDFLAGS）。
func appTitle() string { return "SSHore " + Version }

// AppInfo 供前端「帮助」展示的应用元信息。
type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Repo    string `json:"repo"`
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
	// 迁移落盘放在 startup 而不是 LoadConfig：读函数不写盘（且文件不存在时
	// LoadConfig 会提前 return，迁移根本不会被触发）。internal/config 没有 dirty
	// 机制，所以这里显式保存一次；MigrateLegacyRecents 幂等，后续启动是 no-op。
	if config.MigrateLegacyRecents(cfg) {
		_ = config.SaveConfig(p, cfg)
	}
	// 预设文件只在"不存在"时生成一次；之后永不改写（用户的注释/顺序必须永久保留）。
	// 判据是**文件存在性**，不是某个标记字段 —— "删条目"与"删文件"语义不同，见 README。
	pp, perr := config.DefaultPresetsPath()
	a.presetsPath = pp
	if perr != nil {
		a.presetsErr = perr
	} else if _, statErr := os.Stat(pp); errors.Is(statErr, os.ErrNotExist) {
		if werr := config.SavePresets(pp, config.PresetsTemplate(toConfigPresets(preset.Defaults()))); werr != nil {
			a.presetsErr = fmt.Errorf("生成默认预设文件 %s 失败: %w", pp, werr)
		}
	} else if _, lerr := config.LoadPresets(pp); lerr != nil {
		// 坏文件：只记录、只降级，绝不覆盖用户文件
		a.presetsErr = lerr
	}
	// Task 13（spec D14）：清理 >7 天且名字含 PartMarker 的本地传输临时文件。
	// 进程崩溃后登记的 knownParts 随进程消失，这些 .part 只能靠陈旧清理兜底。
	//
	// ⚠️ 必须后台执行：本清理会递归扫描 cfg.LocalRecent 里的每个历史目录，而那个列表可能
	// 含盘根（C:\ / D:\）与失效网络盘（Z:\）。win10 真机实测（最近位置含 C:\、
	// C:\Windows\system32、C:\Users\lan、Z:\）这一步耗时 **13.9s**。它一旦同步执行，
	// main.go 里紧随 startup() 的 app.Init()（建立 a.sftp 等控制器）就会被拖到十几秒后；
	// 而这段时间内窗口已渲染、按钮可点、a.sftp 仍是 nil —— 绑定调用 nil 解引用 panic，
	// 又被 Wails dispatcher 的 recover 静默吞掉，用户看到的是「点连接毫无反应、不报错、
	// 状态不变」（2026-09-20 真机定位）。清理只是兜底动作，绝不该挡在控制器装配之前。
	// 目录快照在此同步取出：后台 goroutine 不再触碰 a.cfg，避免与后续设置写入竞争。
	go cleanupStaleLocalPartsAt(a.localRecentPaths())
	// 更新服务：注入全部外部依赖（HTTP/进程/配置读写/事件通道），Init 做残留自检
	// 与轮询调度。必须放在 startup —— 这里才有 ctx，且 Init 会被测试直接调用。
	a.updater = update.New(update.Options{
		Goos: gosruntime.GOOS, Goarch: gosruntime.GOARCH,
		Version: Version, ExePath: exePath(),
		Doer: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 15 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		}},
		Launch:  update.StartDetached,
		Acquire: update.Acquire,
		Emit:    func(event string, payload any) { runtime.EventsEmit(a.ctx, event, payload) },
		Log:     a.logUpdate,
		Config:  a.updateSettings,
		Save:    a.saveUpdateSettings,
	})
	a.updater.Init(ctx)
}

// localRecentPaths 返回 cfg.LocalRecent 里去重后的本地目录快照。
// 只在 startup 同步调用一次，之后交给后台清理，避免后台 goroutine 读 a.cfg 与
// 用户改设置（setAppSettings/saveConfig）构成数据竞争。
func (a *App) localRecentPaths() []string {
	if a.cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	roots := make([]string, 0, len(a.cfg.LocalRecent))
	for _, r := range a.cfg.LocalRecent {
		if r.Path == "" || seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		roots = append(roots, r.Path)
	}
	return roots
}

// cleanupStaleLocalPartsAt 逐个目录清理陈旧本地临时文件（>7 天，仅 IsInternalTemp 名字）。
// 只扫配置里记录过的本地目录（用户机器上没有可枚举「我们写过的所有目标目录」的全局索引）；
// 不可读/不存在的目录静默跳过，绝不阻断启动。
//
// 声明为包级变量：单测用它注入阻塞实现，钉住「startup 不被这步挡住」这条回归
// （见 TestStartupDoesNotBlockOnStalePartCleanup）。
var cleanupStaleLocalPartsAt = func(roots []string) {
	now := time.Now()
	for _, root := range roots {
		_, _ = sftp.CleanupStaleLocalParts(root, now)
	}
}

// Init wires controllers. emit forwards subsystem events to the frontend.
// forward needs a Spawner (long-lived ssh -N); sftp needs a blocking Runner (one-shot ops).
func (a *App) Init(emit func(forward.Event)) {
	a.emit = emit
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit, nil)
	// 选择器懒解析配置（Task 5）：app_test 直接构造时 a.cfg 可能为 nil，
	// 闭包必须回退内置默认而不是 panic（三审 R11）。
	a.sftp = sftp.NewCtrlWith(osutil.NewRunner(), emit, a.sftpTransport)
	if dir := stateDir(); dir != "" {
		a.sftp.SetJournalDir(dir) // Task 8 的 backup-swap journal（S6）
	}
	transfer, lister := sync.NewSftpAdapter(a.sftp)
	a.sync = sync.NewCtrl(sync.Deps{
		Spawner:  osutil.NewStreamer(),
		Runner:   osutil.NewCtxRunner(),
		Transfer: transfer,
		ListMany: lister,
		Emit:     emit,
		StateDir: stateDir(),
	})
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
	// 预设文件的问题同样补发（坏文件只降级不覆盖，用户需要看到原因）
	if a.presetsErr != nil && a.emit != nil {
		a.emit(forward.Event{
			SourceType: "system",
			SourceID:   "app",
			TS:         time.Now().Format(time.RFC3339),
			Level:      "error",
			Message:    a.presetsErr.Error(),
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

// GetAppInfo returns app metadata (name/version/repo) for the help panel.
func (a *App) GetAppInfo() AppInfo {
	return AppInfo{Name: "SSHore", Version: Version, Repo: Repo}
}

// ─── 更新检查与下载（Task 11）──────────────────────────────────────────────
// 契约见 spec §5.2；9 个绑定全部 nil 安全：测试直接构造的 App{} 没走 startup，
// a.updater 与 a.ctx 都可能为 nil，必须返回 disabled 快照或明确错误，绝不 panic。

// GetUpdateInfo 返回当前更新状态快照（seq 供前端丢弃迟到载荷）。
func (a *App) GetUpdateInfo() update.UpdateInfo {
	if a.updater == nil {
		return update.UpdateInfo{State: update.StateDisabled, Current: Version, Source: update.DefaultSource}
	}
	return a.updater.Info()
}

// CheckUpdate 执行一次检查；manual=true 绕过门卫与调度约束。
func (a *App) CheckUpdate(manual bool) (update.UpdateInfo, error) {
	if a.updater == nil || a.ctx == nil {
		return a.GetUpdateInfo(), errors.New("更新服务未初始化")
	}
	return a.updater.Check(a.ctx, manual)
}

// StartUpdateDownload 在 available/verify-failed/io-failed 上启动下载。
func (a *App) StartUpdateDownload() error {
	if a.updater == nil || a.ctx == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.StartDownload(a.ctx)
}

// CancelUpdateDownload 取消在飞下载；没有在飞下载时返回 false（幂等）。
func (a *App) CancelUpdateDownload() bool {
	if a.updater == nil || a.ctx == nil {
		return false
	}
	return a.updater.CancelDownload()
}

// ApplyUpdateAndRestart 拉起替换脚本并退出应用。只有脚本成功启动才退出；
// 失败（锁被占、校验不过等）必须原样返回错误让前端展示，绝不静默退出。
func (a *App) ApplyUpdateAndRestart() error {
	if a.updater == nil || a.ctx == nil {
		return errors.New("更新服务未初始化")
	}
	if err := a.updater.ApplyAndRestart(a.ctx); err != nil {
		return err
	}
	runtime.Quit(a.ctx)
	return nil
}

// DiscardUpdateDownload 删除待安装文件与 sidecar，回到 available / idle。
func (a *App) DiscardUpdateDownload() error {
	if a.updater == nil || a.ctx == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.DiscardPending()
}

// SkipUpdateVersion 记录「跳过此版本」，只经 Save 写配置（避免前端快照回写清掉它）。
func (a *App) SkipUpdateVersion(version string) error {
	if a.updater == nil || a.ctx == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.SkipVersion(version)
}

// ClearSkippedUpdate 清掉「跳过此版本」并立刻重查。
func (a *App) ClearSkippedUpdate() error {
	if a.updater == nil || a.ctx == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.ClearSkipped()
}

// OpenReleasePage 用系统浏览器打开发布页；未启动（ctx 缺失）时返回错误而不是 panic。
func (a *App) OpenReleasePage() error {
	if a.ctx == nil {
		return errors.New("应用未初始化")
	}
	runtime.BrowserOpenURL(a.ctx, Repo+"/releases")
	return nil
}

// appSettings 在读锁下返回 a.cfg.App 的副本；cfg 尚未加载时回退到默认设置。
// 更新服务 goroutine 与 Wails 调用线程共用此入口，避免无同步读写 cfg.App。
func (a *App) appSettings() config.AppSettings {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	if a.cfg == nil {
		return config.DefaultAppConfig().App
	}
	return a.cfg.App
}

// sftpTransport 在读锁下读 a.cfg.App.SftpTransport（cfg 未加载时回退空串 = 内置默认）。
// 供选择器懒解析配置，避免对 a.cfg.App 的裸读（最终评审 I-3）。
func (a *App) sftpTransport() string {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	if a.cfg == nil {
		return ""
	}
	return a.cfg.App.SftpTransport
}

// autoStartOnLaunch 在读锁下读 a.cfg.App.AutoStartOnLaunch（cfg 未加载时回退 false）。
// AutoStartEnabled 由 main.go 的延迟 goroutine 调用，必须与 setAppSettings 的整结构写同步
// （最终评审 I-3）。
func (a *App) autoStartOnLaunch() bool {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	if a.cfg == nil {
		return false
	}
	return a.cfg.App.AutoStartOnLaunch
}

// setAppSettings 在写锁下整体替换 a.cfg.App；锁内不做 IO，落盘由调用方在解锁后完成。
func (a *App) setAppSettings(s config.AppSettings) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	a.cfg.App = s
}

// updateSettings 经 appSettings 直读配置子集（不经前端快照），供更新服务读取。
func (a *App) updateSettings() update.Settings {
	s := a.appSettings()
	return update.Settings{
		Auto:     s.UpdateCheckAuto,
		Interval: time.Duration(s.UpdateCheckIntervalHours) * time.Hour,
		Source:   s.UpdateSource,
		Skipped:  s.UpdateSkippedVersion,
	}
}

// saveUpdateSettings 只写 UpdateSkippedVersion 并落盘：跳过版本只由后端读写，
// 前端快照全量回写绝不能把它清掉（Task 10 裁定 5）。加锁改字段、解锁后落盘，
// 避免在持锁期间做磁盘 IO。
func (a *App) saveUpdateSettings(s update.Settings) error {
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfg = config.DefaultAppConfig()
	}
	a.cfg.App.UpdateSkippedVersion = s.Skipped
	a.cfgMu.Unlock()
	return a.saveConfig()
}

// logUpdate 把更新服务的诊断信息（门卫原因 / 限流 / 自动检查失败）转发到前端日志面板。
func (a *App) logUpdate(msg string) {
	if a.emit == nil {
		return
	}
	a.emit(forward.Event{
		SourceType: "system",
		SourceID:   "update",
		TS:         time.Now().Format(time.RFC3339),
		Level:      "info",
		Message:    msg,
	})
}

// exePath 返回当前可执行文件绝对路径；失败返回空串（调用方按空路径降级处理）。
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// SyncWindowBackground aligns the native window background with the active theme
// so the light theme doesn't leave a dark window edge. Best-effort: no-op if the
// startup context is missing (e.g. before OnStartup runs).
func (a *App) SyncWindowBackground(theme string) {
	if a.ctx == nil {
		return
	}
	if theme == "light" {
		runtime.WindowSetBackgroundColour(a.ctx, 238, 242, 247, 255) // #eef2f7 (light --bg)
	} else {
		runtime.WindowSetBackgroundColour(a.ctx, 27, 38, 54, 255) // #1b2636 (dark --bg)
	}
}

// GetSettings returns the persisted app settings (theme/font/auto-start),
// normalized so callers never receive empty/invalid values. 经 appSettings 读锁读取，
// 因为更新服务 goroutine 会写 a.cfg.App.UpdateSkippedVersion。
func (a *App) GetSettings() config.AppSettings {
	return a.appSettings()
}

// SetSettings validates and persists app settings. Invalid theme falls back to
// "system"; fontScale is clamped by AppSettings.Normalize().
func (a *App) SetSettings(s config.AppSettings) error {
	s.Normalize()
	if s.Theme != "dark" && s.Theme != "light" {
		s.Theme = "system"
	}
	a.setAppSettings(s)
	// 落盘在锁外：避免在持锁期间做磁盘 IO。
	if err := a.saveConfig(); err != nil {
		return err
	}
	// 保存成功后重建调度：间隔/开关/更新源的改动立即生效（Task 11 裁定 2）。
	if a.updater != nil {
		a.updater.Reconfigure()
	}
	return nil
}

// GetSftpUI 返回 SFTP 视图状态（最后路径 + 双侧排序），非法值已归一：排序键只认
// name|size|modTime，方向恒为 ±1，LastRemoteDirs 保证非 nil（前端不能拿到 null）。
//
// 并发红线（规格 §6.3.4 红线 2）：读锁内**只做值拷贝**，Normalize() 在解锁
// **之后**调用。Normalize 会给 nil map 赋新 map 头 —— 一旦经共享结构原地归一
// （`a.cfg.SftpUI.Normalize()`），那是对共享结构的原地写：与同时持 RLock 的
// snapshotConfig（`cp := *a.cfg` 会读该字段）以及锁外正在跑的 TOML 编码构成数据
// 竞争（实测：该形状下 -race 报 8 处 DATA RACE）。值拷贝 + 解锁后归一是零成本且
// 更耐改的形状（将来把 `s := a.cfg.SftpUI` 换成指针读也不会引入原地写）。
// a.cfg == nil（App 尚未 Init / startup）时返回归一化零值，绝不 panic。
func (a *App) GetSftpUI() config.SftpUIState {
	a.cfgMu.RLock()
	if a.cfg == nil {
		a.cfgMu.RUnlock()
		s := config.SftpUIState{}
		s.Normalize()
		return s
	}
	s := a.cfg.SftpUI
	a.cfgMu.RUnlock()
	s.Normalize()
	return s
}

// SaveSftpUI 归一并落盘 SFTP 视图状态（规格 §9 / §6.3.5）。
//
// 锁与 IO 规约：Normalize() 在加写锁**之前**完成（同 GetSftpUI 的理由，锁内绝不碰
// map 头）；写锁内只做「nil 守卫 + 整结构赋值」，绝不做 IO；落盘在解锁之后。
// 锁内的 nil 守卫照 setAppSettings —— App 未 Init 时 a.cfg 为 nil，少了它这里就是
// 空指针，而 Wails dispatcher 会把 panic 静默 recover，前端只看到「点了没反应、
// 也不报错」（规格 §6.3.4 BF13）。
//
// 语义是**整结构覆盖**：只给 sort 四字段的 state 会把路径字段清零。前端
// saveSnapshot() 必须先 GetSftpUI 读回、只覆盖 sort 四字段再整结构写回（§6.3.3）；
// 由 TestSaveSftpUIWholeStructOverwriteIsPinned 固化，改动此处须同步改前端契约。
func (a *App) SaveSftpUI(s config.SftpUIState) error {
	s.Normalize()
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	a.cfg.SftpUI = s
	a.cfgMu.Unlock()
	return a.saveConfig()
}

// forwardCtrl / syncCtrl 返回已装配的控制器。与 sftpCtrl 同一成因与同一条铁律：
// 应用启动尚未完成（main.go 的 app.Init 还没跑）时必须返回明确错误 / 空值，绝不能
// nil 解引用 panic —— 它会被 Wails dispatcher 的 recover 静默吞掉，前端只看到
// 「点了没反应、也没有报错」（2026-09-20 真机定位）。
func (a *App) forwardCtrl() (*forward.Ctrl, error) {
	if a.forward == nil {
		return nil, errors.New("端口转发尚未就绪（应用仍在初始化），请稍后重试")
	}
	return a.forward, nil
}

func (a *App) syncCtrl() (*sync.Ctrl, error) {
	if a.sync == nil {
		return nil, errors.New("文件同步尚未就绪（应用仍在初始化），请稍后重试")
	}
	return a.sync, nil
}

// TunnelStates 返回各隧道运行态（id → state 字符串），供前端四态圆点渲染。
func (a *App) TunnelStates() map[string]string {
	c, err := a.forwardCtrl()
	if err != nil {
		return map[string]string{}
	}
	return c.States()
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
	fc, ferr := a.forwardCtrl()
	if ferr != nil {
		return ferr
	}
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
		_ = fc.Stop(t.ID)
		t.Enabled = false
	}
	a.cfg.Tunnels[idx] = t
	return a.saveConfig()
}

// DeleteTunnel removes a tunnel (matched by ID), stopping it if running.
func (a *App) DeleteTunnel(id string) error {
	fc, ferr := a.forwardCtrl()
	if ferr != nil {
		return ferr
	}
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
		_ = fc.Stop(id)
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
	fc, ferr := a.forwardCtrl()
	if ferr != nil {
		return ferr
	}
	t, ok := a.findTunnel(id)
	if !ok {
		return errors.New("tunnel not found")
	}
	if err := fc.Start(t); err != nil {
		return err
	}
	t.Enabled = true
	a.updateTunnel(t)
	return a.saveConfig()
}

func (a *App) StopTunnel(id string) error {
	fc, ferr := a.forwardCtrl()
	if ferr != nil {
		return ferr
	}
	if err := fc.Stop(id); err != nil {
		return err
	}
	if t, ok := a.findTunnel(id); ok {
		t.Enabled = false
		a.updateTunnel(t)
		return a.saveConfig()
	}
	return nil
}

// snapshotConfig 在读锁下复制一份 a.cfg 供锁外序列化/落盘使用。
// 锁内只做结构体复制、绝不做 IO；cfg 尚未加载时返回 nil（与旧行为一致）。
// 存在意义：saveConfig 与 setAppSettings/saveUpdateSettings 的整结构写并发时，
// 若直接序列化 a.cfg，toml 编码器会与另一侧的写竞争（Task 11 fix round 2）。
func (a *App) snapshotConfig() *config.AppConfig {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	if a.cfg == nil {
		return nil
	}
	cp := *a.cfg
	return &cp
}

func (a *App) saveConfig() error {
	if a.cfgPath == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			return err
		}
		a.cfgPath = p
	}
	// 先在读锁下取快照，再用副本落盘：避免与 setAppSettings/saveUpdateSettings
	// 的整结构写并发读写 a.cfg（Task 11 fix round 2）。锁内不做 IO。
	return config.SaveConfig(a.cfgPath, a.snapshotConfig())
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
// 设置中的 AutoStartOnLaunch 关闭时整体跳过（用户选择"启动后不自动连接转发通道"）。
func (a *App) AutoStartEnabled() error {
	if !a.autoStartOnLaunch() {
		return nil
	}
	var errs []error
	// 控制器未装配（app.Init 还没跑）时跳过：auto-start 是尽力而为的启动动作，
	// 绝不因生命周期时序 nil 解引用 panic（同 sftpCtrl 的成因，见其注释）。
	if a.forward != nil {
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
	}
	if a.sync != nil {
		for _, rule := range a.cfg.Syncs {
			if !rule.Enabled {
				continue
			}
			if err := a.sync.Start(rule); err != nil {
				msg := fmt.Sprintf("自动启动同步规则 %s 失败: %v", rule.ID, err)
				if a.emit != nil {
					a.emit(forward.Event{SourceType: "sync", SourceID: rule.ID,
						TS: time.Now().Format(time.RFC3339), Level: "error", Message: msg})
				}
				errs = append(errs, errors.New(msg))
			}
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

// sftpCtrl 返回已装配的 SFTP 门面。应用启动尚未完成（main.go 的 app.Init 还没跑）时
// 返回明确错误：绝不能放任 nil 解引用 panic —— 它会被 Wails dispatcher 的 recover
// 静默吞掉，前端只看到「点了没反应、无报错」（2026-09-20 真机定位：startup 里的陈旧
// .part 清理曾把 Init 拖到十几秒后，那段时间 a.sftp 一直是 nil）。
func (a *App) sftpCtrl() (*sftp.Ctrl, error) {
	if a.sftp == nil {
		return nil, errors.New("SFTP 尚未就绪（应用仍在初始化），请稍后重试")
	}
	return a.sftp, nil
}

func (a *App) SftpList(host, user, path string) ([]sftp.Item, error) {
	c, err := a.sftpCtrl()
	if err != nil {
		return nil, err
	}
	return c.List(host, user, path)
}

// —— Task 9：新传输面绑定（id + resume/partPath + 进度事件 + 取消）——
//
// id 由前端生成（runBatch 的 t<seq>-<n>），是取消与续传唯一的关联键；每次调用的 Progress
// 帧都带它。resume/partPath 这轮由前端传 false/""，Task 14 的「续传」按钮才填真实锚点。
// Atomic 一律能力驱动（见 sftpAtomic）：batch 不支持 .part + 提交，传 true 会被后端硬拒。
//
// 调用是阻塞的：Wails 绑定 + sftp:transfer-progress 事件回推（spec §6.1）。

// progressToEvent 把 Progress 翻成前端事件载荷。字段名是前后端唯一契约（spec §6.3）：
// 这里用显式 map 而不是直接序列化 struct —— 前端读 camelCase，且 Done/Total 保持 int64。
func progressToEvent(p sftp.Progress) map[string]any {
	return map[string]any{
		"id": p.ID, "host": p.Host, "direction": string(p.Direction), "name": p.Name,
		"partPath": p.PartPath, "done": p.Done, "total": p.Total,
		"filesDone": p.FilesDone, "filesTotal": p.FilesTotal, "phase": string(p.Phase),
	}
}

// progressEventName 是进度事件名的唯一来源：前端把同一个字面量放在
// frontend/src/utils/queue.js 的 TRANSFER_PROGRESS_EVENT（vitest 钉死），本包测试
// TestTransferProgressEventNamePinnedWithFrontend 断言两边逐字相等 —— 只在一侧改名
// 不会让任何东西编译失败，只会让订阅静默失效。
const progressEventName = "sftp:transfer-progress"

// emitProgress 转发一帧进度到前端。a.ctx 为空（startup 之前）时静默：绝不对 nil context
// 调 runtime.EventsEmit。
func (a *App) emitProgress(p sftp.Progress) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, progressEventName, progressToEvent(p))
	}
}

// sftpAtomic 返回当前后端是否支持原子提交（.part + 提交）。绑定层的 Atomic 只从这里取值，
// 绝不写死 true/false：写死 true 会让 batch 后端（Atomic=false 直写）的传输全盘失败，
// 写死 false 会丢掉原子语义。
func (a *App) sftpAtomic() bool {
	if a.sftp == nil {
		return false
	}
	return a.sftp.AtomicCapable()
}

// SftpGet 下载单个远端文件；resume/partPath 为 Task 11 的续传参数。
func (a *App) SftpGet(id, host, user, remote, local string, resume bool, partPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	if err := c.TransferGet(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local), false)
	return nil
}

// SftpGetDir recursively downloads a remote directory tree (`sftp get -r`).
func (a *App) SftpGetDir(id, host, user, remote, local string, resume bool, partPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	if err := c.TransferGetTree(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local), false)
	return nil
}

// SftpPut 上传单个本地文件；resume/partPath 为 Task 11 的续传参数。
func (a *App) SftpPut(id, host, user, local, remote string, resume bool, partPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	if err := c.TransferPut(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local), false)
	return nil
}
func (a *App) SftpRemove(host, user, path string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Remove(host, user, path)
}
func (a *App) SftpMkdir(host, user, path string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Mkdir(host, user, path)
}
func (a *App) SftpRename(host, user, oldPath, newPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Rename(host, user, oldPath, newPath)
}

// SftpRemoveRecursive 删除远端文件或目录（目录递归）。
// 目录语义见 sftp.RemoveRecursive：BFS 收集 + 单条 '-' 前缀批处理；含 glob 元字符的路径直接拒绝。
func (a *App) SftpRemoveRecursive(host, user, path string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.RemoveRecursive(host, user, path)
}

// SftpPutRecursive 递归上传本地目录到远端目录。
// put -r 在远端同名目录已存在时是**并入**（同名文件被本地内容覆盖），本绑定不加"整树替换"
// 补偿；需要整树替换的调用方须先自行删除远端同名目录（语义与门面 TransferPutTree 一致）。
func (a *App) SftpPutRecursive(id, host, user, local, remoteDir string, resume bool, partPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	if err := c.TransferPutTree(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remoteDir, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remoteDir), filepath.Dir(local), false)
	return nil
}

// SftpTransferCancel 取消指定 id 的传输，返回是否真的取消到了正在跑的传输。
// Task 9 只接线到门面；整批语义（取消当前项 + 停止派发后续项）由前端编排层落实（spec §6.1），
// 真实的「关该传输会话」由 Task 10 的后端注册表提供（batch 恒 false，幂等）。
func (a *App) SftpTransferCancel(id string) bool {
	if a.sftp == nil {
		return false
	}
	return a.sftp.Cancel(id)
}

// SftpMove 语义等于远端 Rename（跨目录移动）。
func (a *App) SftpMove(host, user, oldPath, newPath string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Rename(host, user, oldPath, newPath)
}

// PathInfo 描述一个本地路径（供前端处理系统拖入的文件/目录）。
// Err 非空表示该项 Lstat 失败，前端据此跳过该项。
type PathInfo struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Err   string `json:"err,omitempty"`
}

// StatPaths 批量 Lstat；单项失败不整体失败（前端据此跳过该项并提示）。
// 非常规类型（符号链接 / FIFO / 设备 / socket）同样置 Err：localfs.Copy 对符号链接是**静默跳过**，
// 若这里不报错，前端会把它记成「完成」却什么都没复制（静默假成功）。
func (a *App) StatPaths(paths []string) []PathInfo {
	out := make([]PathInfo, 0, len(paths))
	for _, p := range paths {
		info := PathInfo{Path: p, Name: filepath.Base(p)}
		st, err := os.Lstat(p)
		if err != nil {
			info.Err = err.Error()
			out = append(out, info)
			continue
		}
		// Lstat 不跟随链接，因此 mode 是链接本身的 mode，ModeSymlink 判定成立。
		if !st.IsDir() && !st.Mode().IsRegular() {
			if st.Mode()&os.ModeSymlink != 0 {
				info.Err = "符号链接暂不支持"
			} else {
				info.Err = "非常规文件类型暂不支持"
			}
			out = append(out, info)
			continue
		}
		info.IsDir = st.IsDir()
		if !st.IsDir() {
			info.Size = st.Size()
		}
		out = append(out, info)
	}
	return out
}

// CopyLocal 复制本地文件/目录到 dst。拒绝把 src 复制进它自己的子树。
// 与远端无关：复用 internal/localfs（纯本地、只依赖标准库）。
func (a *App) CopyLocal(src, dst string) error {
	sp, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	dp, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	if sp == dp {
		// 源与目标同一路径：继续下去 copyFile 会先把源文件 O_TRUNC 成 0 字节，
		// 再读到空内容（把文件拖到它自己所在面板即可命中）。
		return nil
	}
	if localfs.IsSubPath(sp, dp) {
		return fmt.Errorf("拒绝复制到自身子树: %s → %s", sp, dp)
	}
	return localfs.Copy(sp, dp)
}

func (a *App) SftpConnect(host string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Connect(host, "")
}
func (a *App) SftpDisconnect(host string) error {
	c, err := a.sftpCtrl()
	if err != nil {
		return err
	}
	return c.Disconnect(host)
}
func (a *App) SftpConnected(host string) bool {
	if a.sftp == nil {
		return false
	}
	return a.sftp.Connected(host)
}

// SftpProbe 是「真探活」：与粘性的 SftpConnected（刻意不看池里是否有活会话）不同，
// 它做一次真实远端往返（GoBackend 走池化会话的 ReadDirContext；legacy batch 走一次列
// 目录且带 ctx 上界），所以主机休眠/断链后它会答 false，前端据此自愈（规格 §8.2）。
// user 恒传 ""：与 SFTP 视图其它调用（SftpList/SftpGet/…）的会话键 host\x00"" 一致。
//
// ⚠️ 调用方必读：探活会经 markSessionConnected 把粘性 connected[host] 置 true，所以
// 「探针答 false」与「SftpConnected 答 true」会同时成立 —— 降级路径必须再调
// SftpDisconnect 清连接意图，否则未连接分支会读到粘性 true 又翻回「已连接」。
func (a *App) SftpProbe(host string) bool {
	if a.sftp == nil {
		return false
	}
	return a.sftp.Probe(context.Background(), host, "")
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
	c, err := a.sftpCtrl()
	if err != nil {
		return "", err
	}
	home, err := c.Home(host, "")
	if err != nil {
		return "", err
	}
	a.recordRecentSFTP(host, home, "", false)
	return home, nil
}

// normDepth：绑定层把 0 当"未设置"归一为 5；-1 表示无限（仓库惯例，UI 不给入口）；>0 原样。
func normDepth(d int) int {
	if d == 0 {
		return 5
	}
	return d
}

type RemoteSearchRequest struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	MaxDepth int    `json:"maxDepth"`
	Limit    int    `json:"limit"`
}

type LocalSearchRequest struct {
	ID       string `json:"id"`
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	MaxDepth int    `json:"maxDepth"`
	Limit    int    `json:"limit"`
}

// LocalSearchOutcome 是本地深搜的结果快照。
// Scanned 恒为 0：WalkDir 不分层，前端对本地不显示"已扫描 N 个目录"的进度。
type LocalSearchOutcome struct {
	Hits       []localfs.Hit `json:"hits"`
	Scanned    int           `json:"scanned"`
	Unreadable int           `json:"unreadable"`
	Truncated  bool          `json:"truncated"`
	Cancelled  bool          `json:"cancelled"`
}

func (a *App) trackSearch(id string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	a.searchMu.Lock()
	if a.searchCancels == nil {
		a.searchCancels = map[string]context.CancelFunc{}
	}
	if old, ok := a.searchCancels[id]; ok {
		old()
	}
	a.searchCancels[id] = cancel
	a.searchMu.Unlock()
	return ctx, cancel
}

// SearchCancel 取消指定搜索；完成后由调用方调用（幂等）。
func (a *App) SearchCancel(id string) {
	a.searchMu.Lock()
	if c, ok := a.searchCancels[id]; ok {
		c()
		delete(a.searchCancels, id)
	}
	a.searchMu.Unlock()
}

// SftpSearch 远端深搜。取消**不**作为 error 返回：Wails 在 err != nil 时会丢弃
// 第一个返回值，前端将拿不到部分结果（spec §3 决策 18）。
func (a *App) SftpSearch(req RemoteSearchRequest) (sftp.SearchOutcome, error) {
	ctx, cancel := a.trackSearch(req.ID)
	defer func() { cancel(); a.SearchCancel(req.ID) }()
	c, cerr := a.sftpCtrl()
	if cerr != nil {
		return sftp.SearchOutcome{}, cerr
	}
	out, err := c.Search(ctx, req.Host, "", req.Root, req.Pattern, normDepth(req.MaxDepth), req.Limit,
		func(scanned int) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "sftp:search-progress", map[string]any{"id": req.ID, "scanned": scanned})
			}
		})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.Cancelled = true
		return out, nil
	}
	return out, err
}

// LocalSearch 本地深搜。localfs.Search 取消时**只**返回 err（没有 Cancelled 字段），
// 这里统一转成 Cancelled=true + nil error，与远端保持一致（否则前端拿不到部分结果）。
func (a *App) LocalSearch(req LocalSearchRequest) (LocalSearchOutcome, error) {
	ctx, cancel := a.trackSearch(req.ID)
	defer func() { cancel(); a.SearchCancel(req.ID) }()
	hits, skipped, truncated, err := localfs.Search(ctx, req.Root, req.Pattern, localfs.SearchOpts{
		MaxDepth: normDepth(req.MaxDepth), Limit: req.Limit,
	})
	out := LocalSearchOutcome{Hits: hits, Unreadable: skipped, Truncated: truncated}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.Cancelled = true
		return out, nil
	}
	return out, err
}

type Locations struct {
	Bookmarks     []config.Bookmark     `json:"bookmarks"`
	LocalRecents  []config.RecentLocal  `json:"localRecents"`
	RemoteRecents []config.RecentRemote `json:"remoteRecents"`
}

// toConfigPresets / presetEntries 是 app 层**唯一**的类型转换点：
// internal/preset 刻意不 import internal/config（叶子包只依赖标准库）。
func toConfigPresets(es []preset.Entry) []config.Preset {
	out := make([]config.Preset, 0, len(es))
	for _, e := range es {
		out = append(out, config.Preset{Name: e.Name, Scope: e.Scope, Host: e.Host, Path: e.Path})
	}
	return out
}

func presetEntries(ps []config.Preset) []preset.Entry {
	out := make([]preset.Entry, 0, len(ps))
	for _, p := range ps {
		out = append(out, preset.Entry{
			Name:  p.Name,
			Scope: config.NormalizeScope(p.Scope), // 与 LoadPresets 同一条归一规则，双保险
			Host:  p.Host,
			Path:  p.Path,
		})
	}
	return out
}

// Presets 是「📍 位置」下拉里的固定预设：本地面板（local）、Windows 的逻辑盘
// （localDisks）、远程面板（remote）。local/remote 完全来自 presets.toml，
// localDisks 每次实时枚举；remote 条目的 Host 由前端按当前主机过滤。
type Presets struct {
	Local      []preset.Preset `json:"local"`
	LocalDisks []preset.Preset `json:"localDisks"`
	Remote     []preset.Preset `json:"remote"`
	// Err 非空 = presets.toml 读/解析失败（前端据此在日志面板提示用户）。
	// 预设组同时退化为空，但**绝不覆盖**用户文件；不能只靠 startup 的 Init 事件——
	// 那个事件可能早于前端订阅而丢失（Task 8 真机发现）。
	Err string `json:"err,omitempty"`
}

// ListPresets 返回位置下拉的预设：每次调用都重新读 presets.toml 并实时枚举盘符。
// 坏文件不让面板整体失败：预设组退化为空，并通过返回值的 Err 让前端把解析错误写进日志面板
// （startup 阶段也会记录一次，供 Init 时补发事件；两条路径都不改写用户文件）。
// 注意前端只在进入 SFTP 页时拉一次 → 手改文件后需要重启应用生效。
func (a *App) ListPresets() Presets {
	var entries []preset.Entry
	var perr string
	if a.presetsPath != "" {
		ps, err := config.LoadPresets(a.presetsPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				perr = err.Error() // 坏文件：只降级、只报告，绝不覆盖用户文件
			}
			ps = nil
		}
		entries = presetEntries(ps)
	}
	local, disks, remote := preset.All(entries)
	return Presets{Local: local, LocalDisks: disks, Remote: remote, Err: perr}
}

// ListLocations 返回收藏与两侧最近位置。
//
// 并发（规格 §6.3.4 第 3 条 / 审核 M8）：Wails 每次绑定调用起一个 goroutine，而
// Bookmarks/LocalRecent/RemoteRecent 会被上文的四个写入函数改写 —— 因此读锁内
// **只取三个切片头**（写方现在一律构造新 slice 后整结构赋值，切片头一旦发布就不再
// 被原地改写），解锁后再做 nil→空切片的兜底组装。锁内不做任何组装/转换。
func (a *App) ListLocations() Locations {
	a.cfgMu.RLock()
	var bookmarks []config.Bookmark
	var localRecents []config.RecentLocal
	var remoteRecents []config.RecentRemote
	if a.cfg != nil {
		bookmarks = a.cfg.Bookmarks
		localRecents = a.cfg.LocalRecent
		remoteRecents = a.cfg.RemoteRecent
	}
	a.cfgMu.RUnlock()

	out := Locations{
		Bookmarks:     bookmarks,
		LocalRecents:  localRecents,
		RemoteRecents: remoteRecents,
	}
	if out.Bookmarks == nil {
		out.Bookmarks = []config.Bookmark{}
	}
	if out.LocalRecents == nil {
		out.LocalRecents = []config.RecentLocal{}
	}
	if out.RemoteRecents == nil {
		out.RemoteRecents = []config.RecentRemote{}
	}
	return out
}

// AddBookmark 追加一条收藏；同 scope/host/path 已存在则直接返回（不写盘）。
//
// 并发（规格 §6.3.4 红线 3）：**不能**只写 `append(a.cfg.Bookmarks, b)` —— cap 有余量
// 时仍会写进 snapshotConfig 浅拷贝所共享的底层数组，与锁外的 TOML 编码并发即数据竞争。
// 因此构造新数组（拷旧 + 追加）后整结构赋值；写锁内只赋值、无 IO，解锁后再落盘。
func (a *App) AddBookmark(b config.Bookmark) error {
	if b.Path == "" || (b.Scope != "local" && b.Scope != "remote") {
		return errors.New("invalid bookmark")
	}
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	dup := false
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == b.Scope && e.Host == b.Host && e.Path == b.Path {
			dup = true
			break
		}
	}
	if !dup {
		nb := make([]config.Bookmark, 0, len(a.cfg.Bookmarks)+1)
		nb = append(nb, a.cfg.Bookmarks...)
		nb = append(nb, b)
		a.cfg.Bookmarks = nb
	}
	a.cfgMu.Unlock()
	if dup {
		return nil // 去重语义不变：命中即不追加、也不写盘
	}
	return a.saveConfig()
}

// RemoveBookmark 按 scope/host/path 精确删除一条收藏（匹配规则与改造前逐字一致）。
//
// 并发：同 AddBookmark —— 构造新数组逐项过滤，绝不 `a.cfg.Bookmarks[:0]` 原地复用
// 底层数组（那是与锁外 TOML 编码的真数据竞争）。
func (a *App) RemoveBookmark(scope, host, path string) error {
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfgMu.Unlock()
		return nil
	}
	kept := make([]config.Bookmark, 0, len(a.cfg.Bookmarks))
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == scope && e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.Bookmarks = kept
	a.cfgMu.Unlock()
	return a.saveConfig()
}

// —— locations 四个写入点的私有实现（规格 §6.3.1 / §6.3.2 / §6.3.4 / §6.3.5）——
//
// 以下 *Locked 系列**要求调用方已持 cfgMu 写锁**：锁内只构造新值 + 整结构赋值，绝不
// 做 IO，也**绝不调公开绑定**（AddRemoteRecent 等）—— cfgMu 不可重入，同锁重入即死锁。
// 落盘一律由调用方在解锁之后负责。

// rememberRemotePathLocked 把 host 的最后远端目录记为 path，返回内存态是否真的变了。
// 这个读比较只用于短路（省一次落盘），**是否调用本函数由调用方的 rememberLast 门控决定**：
// 若把比较放在门控之外，rememberLast == false 的内部路径会因 map 永不更新而永远判
// changed ⇒ 每个文件一次全量 TOML 编码 + rename（规格 §6.3.2 要避免的写放大回归）。
//
// map 一律**构造新的**（而不是原地 `LastRemoteDirs[host] = path`）：既与锁外 TOML 编码
// 共享不到底层 bucket，也能在裸 `&config.AppConfig{}`（LastRemoteDirs == nil）上安全工作；
// 最后对整个 SftpUI **整结构赋值**（规格 §6.3.4 红线 1）。
func (a *App) rememberRemotePathLocked(host, path string) (changed bool) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	s := a.cfg.SftpUI
	if s.LastRemoteDirs[host] == path { // nil map 的读是安全的
		return false
	}
	dirs := make(map[string]string, len(s.LastRemoteDirs)+1)
	for k, v := range s.LastRemoteDirs {
		dirs[k] = v
	}
	dirs[host] = path
	s.LastRemoteDirs = dirs
	a.cfg.SftpUI = s
	return true
}

// rememberLocalPathLocked 同构：写 LastLocalDir（字符串字段，整结构赋值同样一次到位）。
func (a *App) rememberLocalPathLocked(path string) (changed bool) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	s := a.cfg.SftpUI
	if s.LastLocalDir == path {
		return false
	}
	s.LastLocalDir = path
	a.cfg.SftpUI = s
	return true
}

// addRemoteRecentLocked 是 AddRemoteRecent 的锁内实现。rememberLast 为 true 时才写
// last path —— 门控**必须在 rememberRemotePathLocked 之外**（见其注释的写放大成因）。
// 返回值 changed = 内存态是否真的变了，调用方据此决定要不要落盘。
// 入参守卫（host/path 为空直接返回）、去重置顶与 20 条上限一律照旧。
func (a *App) addRemoteRecentLocked(host, path string, rememberLast bool) (changed bool) {
	if host == "" || path == "" {
		return false
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	// last path 的写入**不受队首短路约束**（规格 §6.3.2）：队首同值只说明 recents 不必
	// 重排，不说明 last path 已正确（升级场景：老配置根本没有 last path）。
	if rememberLast {
		changed = a.rememberRemotePathLocked(host, path)
	}
	// 队首已是同一 (host, path) ⇒ 不再重排/刷新 TS。一次成功传输最多触发两次保存，
	// 批量传输时这是主要写放大来源（100 个文件约 200 次磁盘写）。
	if len(a.cfg.RemoteRecent) > 0 && a.cfg.RemoteRecent[0].Host == host && a.cfg.RemoteRecent[0].Path == path {
		return changed
	}
	rec := config.RecentRemote{Host: host, Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := make([]config.RecentRemote, 0, len(a.cfg.RemoteRecent))
	for _, e := range a.cfg.RemoteRecent {
		if e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	next := append([]config.RecentRemote{rec}, kept...)
	if len(next) > 20 {
		next = next[:20]
	}
	a.cfg.RemoteRecent = next
	return true
}

// addLocalRecentLocked 是 AddLocalRecent 的锁内实现（同构）。本地侧与改造前一致：
// **没有** path 空值守卫，语义不做任何收紧。
func (a *App) addLocalRecentLocked(path string, rememberLast bool) (changed bool) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	if rememberLast {
		changed = a.rememberLocalPathLocked(path)
	}
	// 队首已是同一路径 ⇒ 语义与"去重置顶"完全一致（它本来就会落到队首），
	// 但省掉一次 TOML 全量编码 + tmp + rename。前端 store 有同构短路（T10）。
	if len(a.cfg.LocalRecent) > 0 && a.cfg.LocalRecent[0].Path == path {
		return changed
	}
	rec := config.RecentLocal{Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := make([]config.RecentLocal, 0, len(a.cfg.LocalRecent))
	for _, e := range a.cfg.LocalRecent {
		if e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	next := append([]config.RecentLocal{rec}, kept...)
	if len(next) > 20 {
		next = next[:20]
	}
	a.cfg.LocalRecent = next
	return true
}

// AddLocalRecent 记录一次导航到的本地目录：去重置顶、上限 20，并写 LastLocalDir。
// 公开绑定就是「写锁内 *Locked(..., true) → 解锁 → 仅当 changed 时才 saveConfig()」
// （规格 §6.3.5：锁内无 IO，changed == false 不写盘）。
func (a *App) AddLocalRecent(path string) error {
	a.cfgMu.Lock()
	changed := a.addLocalRecentLocked(path, true)
	a.cfgMu.Unlock()
	if !changed {
		return nil
	}
	return a.saveConfig()
}

// AddRemoteRecent 记录一次导航到的远端目录：去重置顶、上限 20，并写
// LastRemoteDirs[host]。只有**面向用户的导航绑定**能写 last path（规格 §6.3.1）。
func (a *App) AddRemoteRecent(host, path string) error {
	a.cfgMu.Lock()
	changed := a.addRemoteRecentLocked(host, path, true)
	a.cfgMu.Unlock()
	if !changed {
		return nil
	}
	return a.saveConfig()
}

// recordRecentSFTP 记录一次成功的 SFTP 操作：远端目录进 RemoteRecent、本地目录进
// LocalRecent（各自去重置顶、上限 20，见 AddRemoteRecent/AddLocalRecent）；
// 落盘沿用 saveConfig 的 fire-and-forget 模式（见 OnShutdown）。
//
// rememberLast **必须为 false**（规格 §6.3.1，审核 BF1/Blocking）：本函数是后端内部
// 路径，全部调用点是传输完成 4 处与 SftpHome。SftpHome 每次 connect 都被调用，若它顺带
// 写 last path，单主机用户的"最后路径"会被静默改写成 home —— 而前端 store 的队首短路
// 会让紧接着的 addRemoteRecent(记忆路径) 不发 IPC，于是本次会话落点仍正确、盘上已是
// home，验收看不出来（最坏一类静默损坏）。只有面向用户的导航绑定传 true。
//
// 两次 *Locked 在**同一个写锁段**内完成（持锁调 *Locked，不重入公开绑定 ⇒ 不死锁），
// 解锁后按**合并结果**决定是否落盘一次：changed == false ⇒ 直接返回、不写盘
// （§6.3.2 的写盘合并；rememberLast == false 的内部路径因此不会产生任何落盘）。
//
// remoteDir 必须是 **POSIX 语义**的远端目录（调用方用 path.Dir —— 远端没有 Windows
// 反斜杠概念，Windows 上 filepath.Dir("/a/x") 会得到 "\a"）；localDir 是本机目录
// （调用方用 filepath.Dir，Windows 上自然得到反斜杠）。
func (a *App) recordRecentSFTP(host, remoteDir, localDir string, rememberLast bool) {
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	changed := false
	if host != "" && remoteDir != "" {
		changed = a.addRemoteRecentLocked(host, remoteDir, rememberLast)
	}
	if localDir != "" && a.addLocalRecentLocked(localDir, rememberLast) {
		changed = true
	}
	a.cfgMu.Unlock()
	if !changed {
		return
	}
	_ = a.saveConfig()
}

func (a *App) OnShutdown() {
	// OnShutdown 可能早于 Init 被调用（Wails 生命周期边界），此时 cfg 与三个
	// 控制器都可能为 nil，必须逐项守卫而不是直接解引用。
	// 1. 先停同步规则并关闭探测进程
	if a.sync != nil && a.cfg != nil {
		for _, rule := range a.cfg.Syncs {
			_ = a.sync.Stop(rule.ID)
		}
	}
	if a.forward != nil {
		a.forward.OnShutdown()
	}
	// 2. SFTP 生命周期：**顺序是硬约束**（Task 13 / 技术审核 M5，修复轮 1 / F2），
	//    但静默是**有界**的（修复轮 2 / D1）——GoBackend.CloseAll 内部按固定顺序收尾：
	//    先置 closing 拒绝新传输、并在 closeGrace（默认 5s）内等在飞传输自然结束；
	//    宽限期内未结束就强停：取消后端级 transferCtx（中断无界的取额度排队/扫描相）
	//    并关闭全部会话（中断阻塞中的网络 IO）；最后清掉已知 .part（CleanupParts 在关
	//    会话之后自己取新会话）。有界等待是退出 liveness 的硬要求：对端活着但卡死时
	//    传输可能永不返回，无界 Wait 会把退出押在 ssh 的 ServerAlive 超时上。
	//
	//    CloseAll 返回后做 journal 恢复，但**只在正常路径上**才保证没有在飞提交了：
	//    强停路径下某次提交可能恰好停在 rename(target→bak) 与 rename(part→target)
	//    之间，于是恢复探测仍可能撞上这次被强停的提交 —— 这正是有界退出换来的取舍。
	//    RecoverSwaps 因此只按 journal 条目与两端实际存在情况收敛（W0 只删"两端都不
	//    存在/都回滚干净"的条目，能回滚就回滚），绝不把这种条目当成"从未发生"清掉。
	//    顺序颠倒（有传输在飞时先删 .part，或不等静默就恢复）会留下 target 缺失、
	//    bak 残存、journal 条目被清的现场。
	if a.sftp != nil {
		a.sftp.CloseAll()
		// journal 条目自带 host/user；恢复按 (host,user) 分组、只探条目自己的主机。
		// 归属为空的旧条目跳过；主机不可达的那组条目原样保留（下次再试），绝不误清。
		if n, err := a.sftp.RecoverSwaps(); err != nil {
			a.logf("恢复 swap journal 失败: %v", err)
		} else if n > 0 {
			a.logf("恢复了 %d 处中断提交", n)
		}
	}
	// 3. 更新服务：停轮询与在飞下载。OnShutdown 可能早于 startup 被调用，必须守卫。
	if a.updater != nil {
		a.updater.Shutdown()
	}
	if a.cfg != nil {
		_ = a.saveConfig()
	}
}

// logf 发一条 system 事件（emit 未接线时静默），供生命周期路径上报不阻断启动/退出的异常。
func (a *App) logf(format string, args ...any) {
	if a.emit == nil {
		return
	}
	a.emit(forward.Event{
		SourceType: "system",
		SourceID:   "app",
		TS:         time.Now().Format(time.RFC3339),
		Level:      "warn",
		Message:    fmt.Sprintf(format, args...),
	})
}

// 适配器只有一处定义：internal/sync/adapters.go 的 NewSftpAdapter（见 Task 10）。
// 这里不要重复定义，否则 Task 16 的 E2E 测试还要再写一份。

// stateDir 与 DefaultConfigPath 同源：<UserConfigDir>/sshore/state。
func stateDir() string {
	p, err := config.DefaultConfigPath()
	if err != nil {
		return filepath.Join(os.TempDir(), "sshore-state")
	}
	return filepath.Join(filepath.Dir(p), "state")
}

func (a *App) ListSyncRules() []config.SyncRule {
	if a.cfg == nil || a.cfg.Syncs == nil {
		return []config.SyncRule{}
	}
	return a.cfg.Syncs
}

func (a *App) CreateSyncRule(r config.SyncRule) (config.SyncRule, error) {
	if err := sync.ValidateSyncRule(r); err != nil {
		return r, err
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	for _, e := range a.cfg.Syncs {
		if e.Host == r.Host && e.RemotePath == r.RemotePath &&
			e.LocalPath == r.LocalPath && e.Kind == r.Kind {
			return r, errors.New("已存在完全相同的同步规则")
		}
	}
	if r.ID == "" {
		r.ID = config.NewSyncID()
	}
	r.Normalize()
	a.cfg.Syncs = append(a.cfg.Syncs, r)
	return r, a.saveConfig()
}

func (a *App) UpdateSyncRule(r config.SyncRule) error {
	sc, serr := a.syncCtrl()
	if serr != nil {
		return serr
	}
	if err := sync.ValidateSyncRule(r); err != nil {
		return err
	}
	idx := -1
	for i, e := range a.cfg.Syncs {
		if e.ID == r.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("sync rule not found")
	}
	// 运行中的规则先停再存（不做热更新：远端根路径可能整个换掉）。
	_ = sc.Stop(r.ID)
	r.Enabled = false
	a.cfg.Syncs[idx] = r
	return a.saveConfig()
}

func (a *App) DeleteSyncRule(id string) error {
	sc, serr := a.syncCtrl()
	if serr != nil {
		return serr
	}
	idx := -1
	for i, e := range a.cfg.Syncs {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("sync rule not found")
	}
	rule := a.cfg.Syncs[idx]
	_ = sc.Stop(id)
	// 删除规则时一并清理状态文件与本地残留临时文件。
	_ = os.Remove(filepath.Join(stateDir(), "sync-"+id+".json"))
	_, _ = sync.CleanupParts(rule.LocalPath, id)
	a.cfg.Syncs = append(a.cfg.Syncs[:idx], a.cfg.Syncs[idx+1:]...)
	return a.saveConfig()
}

func (a *App) StartSyncRule(id string) error {
	sc, serr := a.syncCtrl()
	if serr != nil {
		return serr
	}
	rule, ok := a.findSyncRule(id)
	if !ok {
		return errors.New("sync rule not found")
	}
	if err := sc.Start(rule); err != nil {
		return err
	}
	rule.Enabled = true
	a.updateSyncRule(rule)
	return a.saveConfig()
}

func (a *App) StopSyncRule(id string) error {
	sc, serr := a.syncCtrl()
	if serr != nil {
		return serr
	}
	if err := sc.Stop(id); err != nil {
		return err
	}
	if rule, ok := a.findSyncRule(id); ok {
		rule.Enabled = false
		a.updateSyncRule(rule)
		return a.saveConfig()
	}
	return nil
}

func (a *App) SyncRuleStates() map[string]string {
	c, err := a.syncCtrl()
	if err != nil {
		return map[string]string{}
	}
	return c.States()
}

func (a *App) SyncRuleStats() map[string]sync.SyncRuleStat {
	c, err := a.syncCtrl()
	if err != nil {
		return map[string]sync.SyncRuleStat{}
	}
	return c.Stats()
}

func (a *App) SyncRuleConflicts(id string) []sync.Conflict {
	c, err := a.syncCtrl()
	if err != nil {
		return []sync.Conflict{}
	}
	return c.Conflicts(id)
}

func (a *App) ResolveSyncConflict(id, relPath, action string) error {
	c, cerr := a.syncCtrl()
	if cerr != nil {
		return cerr
	}
	local, err := localStateOf(a.cfg, id, relPath)
	if err != nil {
		return err
	}
	return c.ResolveConflict(id, relPath, sync.ConflictAction(action), local)
}

func (a *App) ConfirmSyncRuleDeletes(id, fingerprint string) error {
	c, err := a.syncCtrl()
	if err != nil {
		return err
	}
	return c.ConfirmDeletes(id, fingerprint)
}

// RetrySyncRuleFailures 让引擎重试该规则记录在案的失败项。只入队，不在这里传输。
func (a *App) RetrySyncRuleFailures(id string) error {
	c, cerr := a.syncCtrl()
	if cerr != nil {
		return cerr
	}
	_, err := c.RetryFailed(id)
	return err
}

func (a *App) findSyncRule(id string) (config.SyncRule, bool) {
	if a.cfg == nil {
		return config.SyncRule{}, false
	}
	for _, e := range a.cfg.Syncs {
		if e.ID == id {
			return e, true
		}
	}
	return config.SyncRule{}, false
}

func (a *App) updateSyncRule(u config.SyncRule) {
	for i := range a.cfg.Syncs {
		if a.cfg.Syncs[i].ID == u.ID {
			a.cfg.Syncs[i] = u
		}
	}
}

// localStateOf 读取本地文件现状，供冲突裁决使用（只读，不做传输）。
func localStateOf(cfg *config.AppConfig, id, rel string) (sync.LocalState, error) {
	for _, e := range cfg.Syncs {
		if e.ID != id {
			continue
		}
		target, err := sync.LocalTarget(e.LocalPath, rel)
		if err != nil {
			return sync.LocalState{}, err
		}
		st, err := os.Stat(target)
		if err != nil {
			// 只有"确实不存在"才是 Exists=false；权限/符号链接环等错误必须
			// 上抛，否则冲突裁决会把不可读的本地文件当成不存在。
			if errors.Is(err, fs.ErrNotExist) {
				return sync.LocalState{Exists: false}, nil
			}
			return sync.LocalState{}, err
		}
		return sync.LocalState{Exists: true, Size: st.Size(),
			ModTime: sync.FormatModTime(st.ModTime())}, nil
	}
	return sync.LocalState{}, errors.New("sync rule not found")
}
