package osutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// —— OpenSSH 路径解析器的用例 ——
//
// 本文件在 Linux 上必须可跑：解析器的 Windows 分支靠包级 seam（goos/getenv/
// lookPath/stat）注入，绝不依赖宿主真的装了 ssh、也绝不依赖宿主是 Windows。
//
// 期望值一律用 filepath.Join 与实现同法拼装：filepath.Join 的分隔符由**编译目标**
// 决定，测试宿主是 Linux，写死反斜杠会与实现不一致；两边同拼法才与宿主无关。
const testSystemRoot = `C:\Windows`

// fakeInfo 满足 os.FileInfo；解析器只看 IsDir()。
type fakeInfo struct{ dir bool }

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

// lookPathHit / lookPathMiss 是两个常用的 LookPath seam。
func lookPathHit(p string) func(string) (string, error) {
	return func(string) (string, error) { return p, nil }
}

func lookPathMiss() func(string) (string, error) {
	return func(string) (string, error) { return "", exec.ErrNotFound }
}

// statOnlyFiles 返回 stat 替身：只有列出的路径存在，且都是普通文件。
func statOnlyFiles(paths ...string) func(string) (os.FileInfo, error) {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) (os.FileInfo, error) {
		if set[p] {
			return fakeInfo{}, nil
		}
		return nil, os.ErrNotExist
	}
}

// statNoProbe 用于非 Windows 用例：任何文件系统探测都判失败
// （非 Windows 不得走 Windows 回退，这条是"行为不变"的守卫）。
func statNoProbe(t *testing.T) func(string) (os.FileInfo, error) {
	t.Helper()
	return func(p string) (os.FileInfo, error) {
		t.Errorf("非 Windows 分支不得做文件系统探测，却 stat 了 %s", p)
		return nil, errors.New("unexpected stat")
	}
}

// installSeams 注入解析器的四个 seam 并在用例结束时还原。
func installSeams(t *testing.T, hostGOOS string, env map[string]string, lp func(string) (string, error), st func(string) (os.FileInfo, error)) {
	t.Helper()
	prvGOOS, prvGetenv, prvLookPath, prvStat := goos, getenv, lookPath, stat
	t.Cleanup(func() { goos, getenv, lookPath, stat = prvGOOS, prvGetenv, prvLookPath, prvStat })

	goos = hostGOOS
	getenv = func(k string) string { return env[k] }
	lookPath = lp
	stat = st
}

// 1. 环境变量覆盖优先：即使 LookPath 命中，也必须用环境变量。
func TestResolveSSHEnvOverrideWins(t *testing.T) {
	installSeams(t, "linux", map[string]string{"SSHORE_SSH_PATH": "/opt/custom/ssh"}, lookPathHit("/usr/bin/ssh"), statNoProbe(t))
	if got := SSHExecutable(); got != "/opt/custom/ssh" {
		t.Fatalf("SSHExecutable() = %q, want env override", got)
	}
}

func TestResolveSFTPEnvOverrideWins(t *testing.T) {
	installSeams(t, "linux", map[string]string{"SSHORE_SFTP_PATH": "/opt/custom/sftp"}, lookPathHit("/usr/bin/sftp"), statNoProbe(t))
	if got := SFTPExecutable(); got != "/opt/custom/sftp" {
		t.Fatalf("SFTPExecutable() = %q, want env override", got)
	}
}

// 环境变量覆盖在 LookPath 失败时同样生效，且压过 Windows 回退候选。
func TestResolveSFTPEnvOverrideWithoutLookPath(t *testing.T) {
	sysnative := filepath.Join(testSystemRoot, "Sysnative", "OpenSSH", "sftp.exe")
	installSeams(t, "windows", map[string]string{
		"SSHORE_SFTP_PATH": `D:\tools\sftp.exe`,
		"SystemRoot":       testSystemRoot,
	}, lookPathMiss(), statOnlyFiles(sysnative))
	if got := SFTPExecutable(); got != `D:\tools\sftp.exe` {
		t.Fatalf("SFTPExecutable() = %q, want env override", got)
	}
}

// 2a. 非 Windows：LookPath 命中就用它，行为与今天逐字节相同。
func TestResolveSSHUnixLookPathHit(t *testing.T) {
	installSeams(t, "linux", nil, lookPathHit("/usr/bin/ssh"), statNoProbe(t))
	if got := SSHExecutable(); got != "/usr/bin/ssh" {
		t.Fatalf("SSHExecutable() = %q, want /usr/bin/ssh", got)
	}
}

func TestResolveSFTPUnixLookPathHit(t *testing.T) {
	installSeams(t, "darwin", nil, lookPathHit("/usr/local/bin/sftp"), statNoProbe(t))
	if got := SFTPExecutable(); got != "/usr/local/bin/sftp" {
		t.Fatalf("SFTPExecutable() = %q, want /usr/local/bin/sftp", got)
	}
}

// 2b. 非 Windows：LookPath 未命中 ⇒ 回退裸名（等价今天：exec.Command 自己再找一次、
// 找不到就报 "executable file not found in $PATH"），且**不得**探测 Windows 目录。
func TestResolveSSHUnixLookPathMissFallsBackToBareName(t *testing.T) {
	installSeams(t, "linux", nil, lookPathMiss(), statNoProbe(t))
	if got := SSHExecutable(); got != "ssh" {
		t.Fatalf("SSHExecutable() = %q, want bare \"ssh\"", got)
	}
	if got := SFTPExecutable(); got != "sftp" {
		t.Fatalf("SFTPExecutable() = %q, want bare \"sftp\"", got)
	}
}

// 3a. Windows（amd64）等价场景：LookPath 已命中 ⇒ 直接用它，不进回退。
func TestResolveSSHWindowsLookPathHitWins(t *testing.T) {
	hit := `C:\Windows\System32\OpenSSH\ssh.exe`
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot}, lookPathHit(hit), statOnlyFiles())
	if got := SSHExecutable(); got != hit {
		t.Fatalf("SSHExecutable() = %q, want LookPath result %q", got, hit)
	}
}

// 3b. Windows 回退之一：只有 System32\OpenSSH 存在 ⇒ 选中它。
func TestResolveSSHWindowsOnlySystem32(t *testing.T) {
	system32 := filepath.Join(testSystemRoot, "System32", "OpenSSH", "ssh.exe")
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot}, lookPathMiss(), statOnlyFiles(system32))
	if got := SSHExecutable(); got != system32 {
		t.Fatalf("SSHExecutable() = %q, want %q", got, system32)
	}
}

// 3c. **本 bug 的回归判据**：Sysnative 与 System32\OpenSSH 都存在 ⇒ 必须选 Sysnative。
// Sysnative 是从 32 位（windows/386）进程触达 64 位 System32 的唯一入口。
func TestResolveSSHWindowsSysnativeBeatsSystem32(t *testing.T) {
	sysnative := filepath.Join(testSystemRoot, "Sysnative", "OpenSSH", "ssh.exe")
	system32 := filepath.Join(testSystemRoot, "System32", "OpenSSH", "ssh.exe")
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot}, lookPathMiss(), statOnlyFiles(sysnative, system32))
	if got := SSHExecutable(); got != sysnative {
		t.Fatalf("SSHExecutable() = %q, want Sysnative %q（Sysnative 必须排在 System32 之前）", got, sysnative)
	}
}

// sftp 走同一条解析链（batch 后端的 ControlMaster 通道用 ssh、批量通道用 sftp）。
func TestResolveSFTPWindowsSysnativeBeatsSystem32(t *testing.T) {
	sysnative := filepath.Join(testSystemRoot, "Sysnative", "OpenSSH", "sftp.exe")
	system32 := filepath.Join(testSystemRoot, "System32", "OpenSSH", "sftp.exe")
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot}, lookPathMiss(), statOnlyFiles(sysnative, system32))
	if got := SFTPExecutable(); got != sysnative {
		t.Fatalf("SFTPExecutable() = %q, want Sysnative %q", got, sysnative)
	}
}

// 3d. 候选路径存在但是目录 ⇒ 跳过，继续往下找。
func TestResolveSSHWindowsSkipsDirectory(t *testing.T) {
	dirAsExe := filepath.Join(testSystemRoot, "System32", "OpenSSH", "ssh.exe")
	pf := filepath.Join(`C:\Program Files`, "OpenSSH", "ssh.exe")
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot, "ProgramFiles": `C:\Program Files`}, lookPathMiss(),
		func(p string) (os.FileInfo, error) {
			switch p {
			case dirAsExe:
				return fakeInfo{dir: true}, nil
			case pf:
				return fakeInfo{}, nil
			}
			return nil, os.ErrNotExist
		})
	if got := SSHExecutable(); got != pf {
		t.Fatalf("SSHExecutable() = %q, want %q", got, pf)
	}
}

// 3e. Windows 回退的最后一站：%ProgramFiles%\OpenSSH\<name>.exe。
func TestResolveSSHWindowsProgramFilesFallback(t *testing.T) {
	pf := filepath.Join(`C:\Program Files`, "OpenSSH", "ssh.exe")
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot, "ProgramFiles": `C:\Program Files`}, lookPathMiss(), statOnlyFiles(pf))
	if got := SSHExecutable(); got != pf {
		t.Fatalf("SSHExecutable() = %q, want %q", got, pf)
	}
}

// 4. Windows 什么都找不到 ⇒ 回退裸名：不 panic、不返回空串（等价今天的行为，不更差）。
func TestResolveSSHWindowsNothingFoundFallsBackToBareName(t *testing.T) {
	installSeams(t, "windows", map[string]string{"SystemRoot": testSystemRoot, "ProgramFiles": `C:\Program Files`}, lookPathMiss(), statOnlyFiles())
	if got := SSHExecutable(); got != "ssh" {
		t.Fatalf("SSHExecutable() = %q, want bare \"ssh\" (非空、非 panic)", got)
	}
	if got := SFTPExecutable(); got != "sftp" {
		t.Fatalf("SFTPExecutable() = %q, want bare \"sftp\"", got)
	}
}

// SystemRoot 缺失时按系统默认 C:\Windows 兜底，不 panic。
func TestResolveSSHWindowsEmptySystemRoot(t *testing.T) {
	sysnative := filepath.Join(`C:\Windows`, "Sysnative", "OpenSSH", "ssh.exe")
	installSeams(t, "windows", nil, lookPathMiss(), statOnlyFiles(sysnative))
	if got := SSHExecutable(); got != sysnative {
		t.Fatalf("SSHExecutable() = %q, want %q", got, sysnative)
	}
}

// 接线守卫：真实 exec 层只重写 ssh/sftp，其它名字（inotifywait、测试 helper 进程、
// 更新脚本……）必须原样透传，保证既有行为逐字节不变。
func TestResolveExecNameLeavesOthersUntouched(t *testing.T) {
	for _, name := range []string{"inotifywait", "sh", "/usr/bin/ssh", "ssh.exe", "sftp.exe", "", "cmd"} {
		if got := resolveExecName(name); got != name {
			t.Errorf("resolveExecName(%q) = %q, want 原样透传", name, got)
		}
	}
}

func TestResolveExecNameRewritesOpenSSH(t *testing.T) {
	byName := func(n string) (string, error) { return "/usr/bin/" + n, nil }
	installSeams(t, "linux", nil, byName, statNoProbe(t))
	if got := resolveExecName("ssh"); got != "/usr/bin/ssh" {
		t.Errorf("resolveExecName(\"ssh\") = %q, want /usr/bin/ssh", got)
	}
	if got := resolveExecName("sftp"); got != "/usr/bin/sftp" {
		t.Errorf("resolveExecName(\"sftp\") = %q, want /usr/bin/sftp", got)
	}
}

// —— 接线冒烟：真实 exec 层确实经过解析器 ——
//
// 不用 seam，改走生产 getenv：把 SSHORE_SSH_PATH / SSHORE_SFTP_PATH 指向本测试二进制
// （helper 模式，认不出的 argv 直接以 0 退出，见 helper_test.go），若四个真实 exec 层
// （NewRunner / NewCtxRunner / NewSpawner / StartPipes）都跑起来了，就证明
// "ssh"/"sftp" 在 spawn 前确实被替换成了环境变量给的可执行文件。
func TestRealExecLayerUsesResolvedSSHPath(t *testing.T) {
	self := helperExe(t)
	t.Setenv("SSHORE_SSH_PATH", self)

	if out, err := NewRunner()("ssh"); err != nil || out.ExitCode != 0 {
		t.Fatalf("NewRunner()(\"ssh\") = %+v, err=%v；真实 exec 层没走解析器?", out, err)
	}
	if out, err := NewCtxRunner()(context.Background(), "ssh"); err != nil || out.ExitCode != 0 {
		t.Fatalf("NewCtxRunner()(\"ssh\") = %+v, err=%v；真实 exec 层没走解析器?", out, err)
	}
	sp, err := NewSpawner().Start("ssh", nil, nil)
	if err != nil {
		t.Fatalf("NewSpawner().Start(\"ssh\") 失败: %v", err)
	}
	if out := sp.Wait(); out.ExitCode != 0 {
		t.Fatalf("NewSpawner().Start(\"ssh\") ExitCode = %d, want 0", out.ExitCode)
	}
	pp, err := StartPipes("ssh")
	if err != nil {
		t.Fatalf("StartPipes(\"ssh\") 失败: %v", err)
	}
	if out := pp.Wait(); out.ExitCode != 0 {
		t.Fatalf("StartPipes(\"ssh\") ExitCode = %d, want 0", out.ExitCode)
	}
	_ = pp.Close()
}

func TestRealExecLayerUsesResolvedSFTPPath(t *testing.T) {
	self := helperExe(t)
	t.Setenv("SSHORE_SFTP_PATH", self)
	if out, err := NewRunner()("sftp"); err != nil || out.ExitCode != 0 {
		t.Fatalf("NewRunner()(\"sftp\") = %+v, err=%v；真实 exec 层没走解析器?", out, err)
	}
}

// 非 ssh/sftp 的名字必须逐字透传（名字与 argv 都逐字到达子进程）。
func TestRealExecLayerPassesThroughOtherNames(t *testing.T) {
	self := helperExe(t)
	// CtxRunner 的契约：非 0 退出不算 err、退出码如实回传，正好用来钉住透传。
	out, err := NewCtxRunner()(context.Background(), self, "exit", "7")
	if err != nil || out.ExitCode != 7 {
		t.Fatalf("NewCtxRunner()(self, \"exit\", \"7\") = %+v, err=%v, want ExitCode 7", out, err)
	}
}
