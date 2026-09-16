// internal/preset/preset_test.go
package preset

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func names(ps []Preset) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name+"="+p.Path)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("长度不符：\n got %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项不符：\n got %v\nwant %v", i, got, want)
		}
	}
}

// 测试替身：Windows 上 KnownFolderPath 会返回真实路径，必须屏蔽掉，否则用例不可复现。
func stubKnownDirs(t *testing.T, m map[string]string) {
	t.Helper()
	old := knownDirsFn
	knownDirsFn = func() map[string]string { return m }
	t.Cleanup(func() { knownDirsFn = old })
}

func TestLocalSkipsMissingDirs(t *testing.T) {
	stubKnownDirs(t, nil)
	home := filepath.Join("home", "u")
	desktop := filepath.Join(home, "Desktop")
	docs := filepath.Join(home, "Documents")
	exists := map[string]bool{home: true, desktop: true, docs: true} // Downloads 不存在
	got := Local(home, func(p string) bool { return exists[p] })

	want := []string{"主目录=" + home, "桌面=" + desktop, "文档=" + docs}
	if runtime.GOOS != "windows" {
		want = append(want, "根目录=/")
	}
	eq(t, names(got), want)
}

func TestLocalPrefersKnownDirs(t *testing.T) {
	// 桌面被 OneDrive 重定向时，必须用 KnownFolderPath 给的真实路径，
	// 而不是拼 home\Desktop 给出一个不存在的预设。
	real := filepath.Join("Z:", "OneDrive", "桌面")
	stubKnownDirs(t, map[string]string{"Desktop": real})
	home := filepath.Join("home", "u")
	got := Local(home, func(p string) bool { return p == home || p == real })

	want := []string{"主目录=" + home, "桌面=" + real}
	if runtime.GOOS != "windows" {
		want = append(want, "根目录=/") // 非 Windows 由 rootPresets() 追加
	}
	eq(t, names(got), want)
}

func TestDrives(t *testing.T) {
	// bit0=A: bit2=C: bit3=D:
	eq(t, names(Drives(1|4|8)), []string{`A:=A:\`, `C:=C:\`, `D:=D:\`})
	if got := Drives(0); got == nil || len(got) != 0 {
		t.Fatalf("空磁盘组必须是非 nil 空切片（nil 经 JSON 变 null）：%#v", got)
	}
	if got := Drives(1 << 25); len(got) != 1 || got[0].Path != `Z:\` {
		t.Fatalf("bit25 必须是 Z:\\，实得 %v", names(got))
	}
}

func TestUserFilterDedupeAndOrder(t *testing.T) {
	user := []Entry{
		// 完全重复（name+path+host 全同）→ 只保留先出现的
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		// name 留空 → 用 path 显示
		{Name: "", Scope: "local", Path: "/work/noname"},
		// path 为空 → 忽略
		{Name: "坏条目", Scope: "local", Path: ""},
		// scope 非法 → 忽略
		{Name: "错 scope", Scope: "somewhere", Path: "/x"},
		// 同一路径、不同名字 → 都保留（用户显式意图）
		{Name: "重复", Scope: "local", Path: "/work/proj"},
		{Name: "远端日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	}
	eq(t, names(User("local", user)), []string{
		"项目=/work/proj",
		"/work/noname=/work/noname",
		"重复=/work/proj",
	})
	got := User("remote", user)
	if len(got) != 1 || got[0].Path != "/var/log" || got[0].Host != "prod" {
		t.Fatalf("远端自定义预设必须带 host 透传：%+v", got)
	}
}

func TestExpandLocalTilde(t *testing.T) {
	home := filepath.Join("home", "u")
	got := expandLocalTilde([]Entry{
		{Name: "a", Scope: "local", Path: "~/work"},
		{Name: "b", Scope: "local", Path: "~"},
		{Name: "c", Scope: "remote", Path: "~"}, // 远端 sentinel 不得被展开
		{Name: "d", Scope: "local", Path: "/abs"},
	}, home)
	want := []string{filepath.Join(home, "work"), home, "~", "/abs"}
	for i, w := range want {
		if got[i].Path != w {
			t.Fatalf("第 %d 条展开错：got %q want %q", i, got[i].Path, w)
		}
	}
	if kept := expandLocalTilde([]Entry{{Name: "a", Scope: "local", Path: "~/x"}}, ""); len(kept) != 1 || kept[0].Path != "~/x" {
		t.Fatalf("home 为空时必须原样返回：%+v", kept)
	}
}

func TestAllUsesConfigEntriesOnly(t *testing.T) {
	stubKnownDirs(t, nil)
	local, _, remote := All([]Entry{
		{Name: "项目", Scope: "local", Path: "/work/proj"},
		{Name: "生产日志", Scope: "remote", Host: "prod", Path: "/var/log"},
	})
	// 渲染路径不得混入任何硬编码条目：预设组 = 配置文件条目
	eq(t, names(local), []string{"项目=/work/proj"})
	eq(t, names(remote), []string{"生产日志=/var/log"})
}

func TestDefaultsSeedsExistentDirsOnly(t *testing.T) {
	stubKnownDirs(t, nil) // 平台实现置空：桌面/下载/文档 由 home + 英文名拼出并做存在性检查
	got := Defaults()
	if len(got) == 0 {
		t.Fatal("默认预设不能为空")
	}
	scopes := map[string]int{}
	for _, e := range got {
		if e.Scope != "local" && e.Scope != "remote" {
			t.Fatalf("scope 只能是 local/remote：%+v", e)
		}
		if e.Path == "" {
			t.Fatalf("默认预设不得有空 path：%+v", e)
		}
		scopes[e.Scope]++
	}
	if scopes["local"] == 0 || scopes["remote"] == 0 {
		t.Fatalf("默认值必须同时覆盖两个面板：%+v", got)
	}
	// 本地面板的默认项必须真实存在：播种进配置的预设不能一点就报错
	for _, e := range got {
		if e.Scope != "local" {
			continue
		}
		if fi, err := os.Stat(e.Path); err != nil || !fi.IsDir() {
			t.Fatalf("本地默认预设必须是存在的目录：%+v", e)
		}
	}
	found := false
	for _, e := range got {
		if e.Scope == "local" && e.Name == "主目录" {
			found = true
		}
	}
	if !found {
		t.Fatalf("默认值必须包含本地面板的 主目录：%+v", got)
	}
}

func TestRemote(t *testing.T) {
	eq(t, names(Remote()), []string{"主目录=" + RemoteHomeToken, "根目录=/"})
	if RemoteHomeToken != "~" {
		t.Fatalf("sentinel 必须字面量是 ~（前端 REMOTE_HOME 与它配对）：%q", RemoteHomeToken)
	}
}

func TestAllNonNil(t *testing.T) {
	local, disks, remote := All(nil)
	if local == nil || disks == nil || remote == nil {
		t.Fatalf("三组都不得为 nil（配置为空 = 预设组为空，但不能是 null）：%v %v %v", local, disks, remote)
	}
	if len(local) != 0 || len(remote) != 0 {
		t.Fatalf("配置为空时预设组就该是空的：%v %v", names(local), names(remote))
	}
	if runtime.GOOS == "windows" {
		if len(disks) == 0 {
			t.Fatal("Windows 上至少要有一个逻辑盘（C:）")
		}
	} else if len(disks) != 0 {
		t.Fatalf("非 Windows 不应有磁盘组：%v", names(disks))
	}
}
