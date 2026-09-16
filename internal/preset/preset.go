// Package preset 提供「📍 位置」下拉里的三组内容：
//   - 预设组的渲染（User / All）：内容完全来自 presets.toml，首次生成该文件时由 Defaults() 播种；
//   - 默认播种源（Defaults = Local + Remote；**不含盘符**）；
//   - Windows 盘符（Drives，每次实时枚举，不进配置文件）。
//
// 计算逻辑与"目录是否存在"的判定分离（exists 注入），平台差异收在
// platform_windows.go / platform_other.go 里，因此三组预设都能在任何平台上单测；
// 本包不 import internal/config（叶子包只依赖标准库）。
package preset

import (
	"os"
	"path/filepath"
	"strings"
)

// RemoteHomeToken 是远程「主目录」预设的占位路径：远端 home 必须先连上主机、
// 执行 pwd 才能得到，所以下拉里放一个 sentinel，由前端在选中时解析。
// 前端对应常量：SftpView.vue 的 REMOTE_HOME。
const RemoteHomeToken = "~"

// Preset 是位置下拉里的一项。
// Path 必须是"可直接交给列表接口"的路径：Windows 盘符一律带尾反斜杠，
// 否则 "D:" 会被解析为"D 盘的当前目录"而不是盘根。
// Host 只有远程预设会填：留空 = 所有主机（前端按当前主机过滤，规则同书签）。
type Preset struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}

// Entry 是配置文件里的一条预设（app 层从 config.Preset 转换而来）。
// 配置文件是预设组的**唯一数据源**：首次启动由 Defaults() 播种，之后完全由用户维护。
// preset 包刻意不 import internal/config：保持只依赖标准库、便于单测，
// 也避免"数据模型包"反过来依赖"整个应用配置"。
type Entry struct {
	Name  string
	Scope string // local | remote
	Host  string // 仅 scope=remote 有意义
	Path  string
}

// dirSpecs：显示名 → 已知文件夹 key（Windows 的 KnownFolderPath）／英文目录名（其他平台）。
var dirSpecs = []struct{ Name, Key string }{
	{"桌面", "Desktop"},
	{"下载", "Downloads"},
	{"文档", "Documents"},
}

// knownDirsFn 是平台实现的测试接缝：Windows 上 knownDirs() 会返回真实路径，
// 单测必须能把它换成固定值。
var knownDirsFn = knownDirs

// User 把配置文件条目按面板过滤成**该面板的全部预设**（渲染路径上唯一的数据源）：
//   - scope 不匹配或 path 为空的条目**静默忽略**——手改配置写错一条，
//     不能让整个位置下拉消失，也不能让应用报错；
//   - name 留空时用 path 兜底显示；
//   - **完全相同**的 (name, path, host) 只保留先出现的那条（重复粘贴通常是无意的）；
//     但同一路径配不同名字都保留——v3 下配置是预设的唯一来源，不能替用户丢掉一个。
func User(scope string, entries []Entry) []Preset {
	out := []Preset{}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Scope != scope || e.Path == "" {
			continue
		}
		name := e.Name
		if name == "" {
			name = e.Path
		}
		k := name + "\x00" + e.Path + "\x00" + e.Host
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Preset{Name: name, Path: e.Path, Host: e.Host})
	}
	return out
}

// Defaults 返回"首次生成 presets.toml 时写进模板"的默认预设（见 config.PresetsTemplate）：
// 本地 主目录/桌面/下载/文档（+ 非 Windows 的 根目录）、远程 主目录(~)/根目录。
// 只播种**实际存在**的本地目录，且**不含盘符**——磁盘组每次实时枚举，不进配置。
func Defaults() []Entry {
	home, _ := os.UserHomeDir()
	out := []Entry{}
	for _, p := range Local(home, dirExists) {
		out = append(out, Entry{Name: p.Name, Scope: "local", Path: p.Path})
	}
	for _, p := range Remote() {
		out = append(out, Entry{Name: p.Name, Scope: "remote", Path: p.Path})
	}
	return out
}

// Local 返回本地面板的默认预设项（只被 Defaults 用作播种源；渲染路径不调用它）。
// exists 为目录存在性判定（注入以便单测）；不存在的目录被静默跳过——
// 宁可不显示，也不能给一个点进去就报错的预设。
func Local(home string, exists func(string) bool) []Preset {
	out := []Preset{}
	if home != "" && exists(home) {
		out = append(out, Preset{Name: "主目录", Path: home})
	}
	known := knownDirsFn()
	for _, d := range dirSpecs {
		p := known[d.Key]
		if p == "" && home != "" {
			p = filepath.Join(home, d.Key)
		}
		if p != "" && exists(p) {
			out = append(out, Preset{Name: d.Name, Path: p})
		}
	}
	return append(out, rootPresets()...)
}

// Drives 把 GetLogicalDrives 的位掩码翻译成盘符预设（bit0=A: … bit25=Z:）。
func Drives(mask uint32) []Preset {
	out := []Preset{}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		out = append(out, Preset{Name: letter + ":", Path: letter + `:\`})
	}
	return out
}

// Remote 返回远程面板的固定预设：主目录（sentinel，选中后由前端解析）与根目录。
func Remote() []Preset {
	return []Preset{
		{Name: "主目录", Path: RemoteHomeToken},
		{Name: "根目录", Path: "/"},
	}
}

// All 组装位置下拉需要的三组预设。预设组（local/remote）**完全来自配置文件条目**；
// 磁盘组每次实时枚举（盘符不进配置：插拔 U 盘后重开面板就该看到新盘）。
// 三组都保证非 nil：nil 切片经 JSON 会变成 null，前端就得处处判空。
func All(user []Entry) (local, disks, remote []Preset) {
	home, _ := os.UserHomeDir()
	// 本地面板的 "~" / "~/xxx" 展开为主目录；远端条目的 "~" 是 sentinel，必须原样保留。
	return User("local", expandLocalTilde(user, home)), Drives(logicalDriveMask()), User("remote", user)
}

// expandLocalTilde 把 local 条目里 "~" / "~/xxx" 的 path 展开成 home：
// 手写配置时 "~/work" 是最自然的写法，不展开就等于"点了就报错"。
// 远端条目的 "~" 是"连上主机后才解析"的 sentinel，**绝不能**在这里展开。
func expandLocalTilde(entries []Entry, home string) []Entry {
	if home == "" {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Scope == "local" && (e.Path == "~" || strings.HasPrefix(e.Path, "~/")) {
			e.Path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(e.Path, "~"), "/"))
		}
		out = append(out, e)
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
