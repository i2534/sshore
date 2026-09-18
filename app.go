package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
)

type App struct {
	ctx     context.Context
	forward *forward.Ctrl
	sftp    *sftp.Ctrl
	cfg     *config.AppConfig
	cfgPath string
	sync    *sync.Ctrl
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
	a.cleanupStaleParts()
}

// cleanupStaleParts 在启动时清理最近用过的本地目录下的陈旧临时文件（>7 天）。
// 只扫配置里记录的本地目录（用户机器上没有可枚举「我们写过的所有目标目录」的全局索引）；
// 不可读/不存在的目录静默跳过，绝不阻断启动。
func (a *App) cleanupStaleParts() {
	if a.cfg == nil {
		return
	}
	seen := map[string]bool{}
	for _, r := range a.cfg.LocalRecent {
		if r.Path == "" || seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		_, _ = sftp.CleanupStaleLocalParts(r.Path, time.Now())
	}
}

// Init wires controllers. emit forwards subsystem events to the frontend.
// forward needs a Spawner (long-lived ssh -N); sftp needs a blocking Runner (one-shot ops).
func (a *App) Init(emit func(forward.Event)) {
	a.emit = emit
	a.forward = forward.NewCtrl(osutil.NewSpawner(), emit, nil)
	// 选择器懒解析配置（Task 5）：app_test 直接构造时 a.cfg 可能为 nil，
	// 闭包必须回退内置默认而不是 panic（三审 R11）。
	a.sftp = sftp.NewCtrlWith(osutil.NewRunner(), emit, func() string {
		if a.cfg == nil {
			return ""
		}
		return a.cfg.App.SftpTransport
	})
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
// normalized so callers never receive empty/invalid values.
func (a *App) GetSettings() config.AppSettings {
	if a.cfg == nil {
		return config.DefaultAppConfig().App
	}
	return a.cfg.App
}

// SetSettings validates and persists app settings. Invalid theme falls back to
// "system"; fontScale is clamped by AppSettings.Normalize().
func (a *App) SetSettings(s config.AppSettings) error {
	s.Normalize()
	if s.Theme != "dark" && s.Theme != "light" {
		s.Theme = "system"
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	a.cfg.App = s
	return a.saveConfig()
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
// 设置中的 AutoStartOnLaunch 关闭时整体跳过（用户选择"启动后不自动连接转发通道"）。
func (a *App) AutoStartEnabled() error {
	if a.cfg == nil || !a.cfg.App.AutoStartOnLaunch {
		return nil
	}
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
func (a *App) sftpAtomic() bool { return a.sftp.AtomicCapable() }

// SftpGet 下载单个远端文件；resume/partPath 为 Task 11 的续传参数。
func (a *App) SftpGet(id, host, user, remote, local string, resume bool, partPath string) error {
	if err := a.sftp.TransferGet(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local))
	return nil
}

// SftpGetDir recursively downloads a remote directory tree (`sftp get -r`).
func (a *App) SftpGetDir(id, host, user, remote, local string, resume bool, partPath string) error {
	if err := a.sftp.TransferGetTree(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local))
	return nil
}

// SftpPut 上传单个本地文件；resume/partPath 为 Task 11 的续传参数。
func (a *App) SftpPut(id, host, user, local, remote string, resume bool, partPath string) error {
	if err := a.sftp.TransferPut(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local))
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

// SftpRemoveRecursive 删除远端文件或目录（目录递归）。
// 目录语义见 sftp.RemoveRecursive：BFS 收集 + 单条 '-' 前缀批处理；含 glob 元字符的路径直接拒绝。
func (a *App) SftpRemoveRecursive(host, user, path string) error {
	return a.sftp.RemoveRecursive(host, user, path)
}

// SftpPutRecursive 递归上传本地目录到远端目录。
// put -r 在远端同名目录已存在时是**并入**（同名文件被本地内容覆盖），本绑定不加"整树替换"
// 补偿；需要整树替换的调用方须先自行删除远端同名目录（语义与门面 TransferPutTree 一致）。
func (a *App) SftpPutRecursive(id, host, user, local, remoteDir string, resume bool, partPath string) error {
	if err := a.sftp.TransferPutTree(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remoteDir, Local: local, Resume: resume, PartPath: partPath, Atomic: a.sftpAtomic()}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remoteDir), filepath.Dir(local))
	return nil
}

// SftpTransferCancel 取消指定 id 的传输，返回是否真的取消到了正在跑的传输。
// Task 9 只接线到门面；整批语义（取消当前项 + 停止派发后续项）由前端编排层落实（spec §6.1），
// 真实的「关该传输会话」由 Task 10 的后端注册表提供（batch 恒 false，幂等）。
func (a *App) SftpTransferCancel(id string) bool { return a.sftp.Cancel(id) }

// SftpMove 语义等于远端 Rename（跨目录移动）。
func (a *App) SftpMove(host, user, oldPath, newPath string) error {
	return a.sftp.Rename(host, user, oldPath, newPath)
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
	out, err := a.sftp.Search(ctx, req.Host, "", req.Root, req.Pattern, normDepth(req.MaxDepth), req.Limit,
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

func (a *App) ListLocations() Locations {
	if a.cfg == nil {
		return Locations{Bookmarks: []config.Bookmark{}, LocalRecents: []config.RecentLocal{}, RemoteRecents: []config.RecentRemote{}}
	}
	out := Locations{
		Bookmarks:     a.cfg.Bookmarks,
		LocalRecents:  a.cfg.LocalRecent,
		RemoteRecents: a.cfg.RemoteRecent,
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

func (a *App) AddBookmark(b config.Bookmark) error {
	if b.Path == "" || (b.Scope != "local" && b.Scope != "remote") {
		return errors.New("invalid bookmark")
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == b.Scope && e.Host == b.Host && e.Path == b.Path {
			return nil
		}
	}
	a.cfg.Bookmarks = append(a.cfg.Bookmarks, b)
	return a.saveConfig()
}

func (a *App) RemoveBookmark(scope, host, path string) error {
	if a.cfg == nil {
		return nil
	}
	kept := a.cfg.Bookmarks[:0]
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == scope && e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.Bookmarks = kept
	return a.saveConfig()
}

func (a *App) AddLocalRecent(path string) error {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	// 队首已是同一路径 ⇒ 直接返回：语义与"去重置顶"完全一致（它本来就会落到队首），
	// 但省掉一次 TOML 全量编码 + tmp + rename。前端 store 有同构短路（T10）。
	if len(a.cfg.LocalRecent) > 0 && a.cfg.LocalRecent[0].Path == path {
		return nil
	}
	rec := config.RecentLocal{Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := a.cfg.LocalRecent[:0]
	for _, e := range a.cfg.LocalRecent {
		if e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.LocalRecent = append([]config.RecentLocal{rec}, kept...)
	if len(a.cfg.LocalRecent) > 20 {
		a.cfg.LocalRecent = a.cfg.LocalRecent[:20]
	}
	return a.saveConfig()
}

func (a *App) AddRemoteRecent(host, path string) error {
	if host == "" || path == "" {
		return nil
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	// 同上：队首已是同一 (host, path) 时不再写盘。一次成功传输最多触发两次保存，
	// 批量传输时这是主要写放大来源（100 个文件约 200 次磁盘写）。
	if len(a.cfg.RemoteRecent) > 0 && a.cfg.RemoteRecent[0].Host == host && a.cfg.RemoteRecent[0].Path == path {
		return nil
	}
	rec := config.RecentRemote{Host: host, Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := a.cfg.RemoteRecent[:0]
	for _, e := range a.cfg.RemoteRecent {
		if e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.RemoteRecent = append([]config.RecentRemote{rec}, kept...)
	if len(a.cfg.RemoteRecent) > 20 {
		a.cfg.RemoteRecent = a.cfg.RemoteRecent[:20]
	}
	return a.saveConfig()
}

// recordRecentSFTP 记录一次成功的 SFTP 操作：远端目录进 RemoteRecent、本地目录进
// LocalRecent（各自去重置顶、上限 20，见 AddRemoteRecent/AddLocalRecent）；
// 落盘沿用 saveConfig 的 fire-and-forget 模式（见 OnShutdown）。
// remoteDir 必须是 **POSIX 语义**的远端目录（调用方用 path.Dir —— 远端没有 Windows
// 反斜杠概念，Windows 上 filepath.Dir("/a/x") 会得到 "\a"）；localDir 是本机目录
// （调用方用 filepath.Dir，Windows 上自然得到反斜杠）。
func (a *App) recordRecentSFTP(host, remoteDir, localDir string) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	if host != "" && remoteDir != "" {
		_ = a.AddRemoteRecent(host, remoteDir)
	}
	if localDir != "" {
		_ = a.AddLocalRecent(localDir)
	}
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
	_ = a.sync.Stop(r.ID)
	r.Enabled = false
	a.cfg.Syncs[idx] = r
	return a.saveConfig()
}

func (a *App) DeleteSyncRule(id string) error {
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
	_ = a.sync.Stop(id)
	// 删除规则时一并清理状态文件与本地残留临时文件。
	_ = os.Remove(filepath.Join(stateDir(), "sync-"+id+".json"))
	_, _ = sync.CleanupParts(rule.LocalPath, id)
	a.cfg.Syncs = append(a.cfg.Syncs[:idx], a.cfg.Syncs[idx+1:]...)
	return a.saveConfig()
}

func (a *App) StartSyncRule(id string) error {
	rule, ok := a.findSyncRule(id)
	if !ok {
		return errors.New("sync rule not found")
	}
	if err := a.sync.Start(rule); err != nil {
		return err
	}
	rule.Enabled = true
	a.updateSyncRule(rule)
	return a.saveConfig()
}

func (a *App) StopSyncRule(id string) error {
	if err := a.sync.Stop(id); err != nil {
		return err
	}
	if rule, ok := a.findSyncRule(id); ok {
		rule.Enabled = false
		a.updateSyncRule(rule)
		return a.saveConfig()
	}
	return nil
}

func (a *App) SyncRuleStates() map[string]string           { return a.sync.States() }
func (a *App) SyncRuleStats() map[string]sync.SyncRuleStat { return a.sync.Stats() }
func (a *App) SyncRuleConflicts(id string) []sync.Conflict { return a.sync.Conflicts(id) }

func (a *App) ResolveSyncConflict(id, relPath, action string) error {
	local, err := localStateOf(a.cfg, id, relPath)
	if err != nil {
		return err
	}
	return a.sync.ResolveConflict(id, relPath, sync.ConflictAction(action), local)
}

func (a *App) ConfirmSyncRuleDeletes(id, fingerprint string) error {
	return a.sync.ConfirmDeletes(id, fingerprint)
}

// RetrySyncRuleFailures 让引擎重试该规则记录在案的失败项。只入队，不在这里传输。
func (a *App) RetrySyncRuleFailures(id string) error {
	_, err := a.sync.RetryFailed(id)
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
