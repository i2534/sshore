package update

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultWait 是脚本等待旧进程退出的默认上限。
const DefaultWait = 60 * time.Second

// Plan 是一次升级要用到的全部路径与参数。
type Plan struct {
	Target  string
	Pending string
	Backup  string
	Sidecar string
	LogPath string
	ExeDir  string
	Size    int64
	Wait    time.Duration
}

// PlanFor 按平台与版本算出命名；非 release 的当前版本用时间戳备份名。
//
// 命名保留版本串原样（含可选的 v 前缀），以便 ParsePendingName 能无损往返：
// Base 会剥掉 v 前缀，若用它拼名则 sshore.v0.7.0 会退化成 sshore.0.7.0，
// 与 spec §8.4 的备份名约定及 plan_test 的期望不符。
func PlanFor(goos, exePath, fromVer, toVer string, size int64, wait time.Duration) Plan {
	if wait <= 0 {
		wait = DefaultWait
	}
	dir := filepath.Dir(exePath)
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	pending := filepath.Join(dir, "sshore."+strings.TrimSpace(toVer)+ext)
	var backup string
	switch Class(fromVer) {
	case KindClean, KindDescribe:
		// 干净 tag 与 git describe 串都能唯一标识被替换的版本（spec §8.4）
		backup = filepath.Join(dir, "sshore."+strings.TrimSpace(fromVer)+ext)
	default:
		// dev / 未知版本没有唯一标识，用时间戳
		backup = filepath.Join(dir, "sshore.dev-"+time.Now().Format("20060102-150405")+ext)
	}
	return Plan{
		Target:  exePath,
		Pending: pending,
		Backup:  backup,
		Sidecar: pending + ".sha256",
		LogPath: filepath.Join(dir, "sshore-update.log"),
		ExeDir:  dir,
		Size:    size,
		Wait:    wait,
	}
}

// IsPendingName 是 pending 的唯一判别（spec §7.5）：干净 tag 且版本序大于当前版本。
//
// 只用版本序判别，绝不用名字模式（如 sshore.v*）：否则版本低于当前的备份
// （sshore.v0.6.0）会被当成待安装文件，在残留恢复时触发降级安装。
// Compare 返回 0 同时表示「相等」与「不可比」，因此必须先用 Class 门控，
// 再依赖 Compare > 0，不能直接用 == 0 判断「无新版本」。
//
// dev 构建（I-2）没有版本序可比：任何干净 tag 都视为待安装（spec §2.8/§9
// 「非 release 构建手动可升级」）。dev 自己的备份名是 sshore.dev-<ts>，
// 其版本段解析为 dev-…、Class 非 Clean，因此不会被误判成 pending。
func IsPendingName(name, current string) bool {
	ver, ok := ParsePendingName(name)
	if !ok || Class(ver) != KindClean {
		return false
	}
	switch Class(current) {
	case KindDev:
		return true
	case KindClean, KindDescribe:
		return Compare(ver, current) > 0
	default:
		return false
	}
}

// ResumePending 在启动自检时找出上次未完成的待安装文件（Size 置 0，由磁盘决定）。
func ResumePending(exeDir, current string) (Plan, bool) {
	entries, err := os.ReadDir(exeDir)
	if err != nil {
		return Plan{}, false
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() || !IsPendingName(e.Name(), current) {
			continue
		}
		if best == "" {
			best = e.Name()
			continue
		}
		bv, _ := ParsePendingName(best)
		v, _ := ParsePendingName(e.Name())
		if Compare(v, bv) > 0 {
			best = e.Name()
		}
	}
	if best == "" {
		return Plan{}, false
	}
	ext := ""
	if strings.HasSuffix(best, ".exe") {
		ext = ".exe"
	}
	toVer, _ := ParsePendingName(best)
	goos := "linux"
	if ext == ".exe" {
		goos = "windows"
	}
	return PlanFor(goos, filepath.Join(exeDir, "sshore"+ext), current, toVer, 0, 0), true
}
