package osutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 解析 OpenSSH 客户端可执行文件（ssh / sftp）。
//
// 背景（真机 bug）：windows/386 构建在 64 位 Windows 上点「连接」报
// `exec: "ssh": executable file not found in %PATH%`。根因是 WOW64 文件系统重定向：
// 32 位进程把 PATH 里的 `C:\Windows\System32` 重定向到 `C:\Windows\SysWOW64`，
// 而后者没有 OpenSSH 目录；真实 ssh.exe 只存在于 64 位的 `System32\OpenSSH`。
// 从 32 位进程触达它的唯一入口是 `Sysnative`。
//
// 解析顺序（resolveOpenSSH）：
//  1. 环境变量 SSHORE_SSH_PATH / SSHORE_SFTP_PATH —— 覆盖一切，给人/测试用；
//  2. exec.LookPath(<name>) —— amd64 与 Linux 都命中这条，行为与今天逐字节相同；
//  3. 仅 Windows 的回退：%SystemRoot%\Sysnative\OpenSSH → %SystemRoot%\System32\OpenSSH
//     → %ProgramFiles%\OpenSSH（**Sysnative 必须排在 System32 之前**，见上）；
//  4. 兜底返回裸名 <name> —— 等价于今天的行为，不会更差。
//
// 非 Windows 只走 1→2→4。

// —— 以下四个包级变量只是 seam，**仅供测试注入**；生产代码从不修改它们 ——
var (
	goos     = runtime.GOOS  // 仅供测试注入
	getenv   = os.Getenv     // 仅供测试注入
	lookPath = exec.LookPath // 仅供测试注入
	stat     = os.Stat       // 仅供测试注入
)

const (
	envSSHPath  = "SSHORE_SSH_PATH"
	envSFTPPath = "SSHORE_SFTP_PATH"
)

// SSHExecutable 返回 ssh 客户端的可执行文件（绝对路径，或兜底的裸名 "ssh"）。
func SSHExecutable() string { return resolveOpenSSH("ssh") }

// SFTPExecutable 返回 sftp 客户端的可执行文件（绝对路径，或兜底的裸名 "sftp"）。
func SFTPExecutable() string { return resolveOpenSSH("sftp") }

// resolveExecName 供 osutil 的真实 exec 层使用：只重写 OpenSSH 客户端
// （ssh/sftp），其它名字（inotifywait、测试 helper 进程、更新脚本……）原样返回
// —— 保证它们的既有行为逐字节不变。
func resolveExecName(name string) string {
	switch name {
	case "ssh":
		return SSHExecutable()
	case "sftp":
		return SFTPExecutable()
	}
	return name
}

// resolveOpenSSH 是解析顺序的唯一实现；两个入口只差一个名字。
func resolveOpenSSH(name string) string {
	if p := strings.TrimSpace(getenv(envKeyFor(name))); p != "" {
		return p
	}
	if p, err := lookPath(name); err == nil && strings.TrimSpace(p) != "" {
		return p
	}
	if goos == "windows" {
		for _, dir := range windowsOpenSSHDirs() {
			cand := filepath.Join(dir, name+".exe")
			if fi, err := stat(cand); err == nil && fi != nil && !fi.IsDir() {
				return cand
			}
		}
	}
	return name
}

// windowsOpenSSHDirs 返回 Windows 回退的候选目录，**顺序即语义**：Sysnative 必须排在
// System32 之前。32 位（windows/386）进程里 System32 被 WOW64 重定向到没有 OpenSSH
// 的 SysWOW64，而 Sysnative 是 32 位进程触达 64 位 System32 的唯一入口 —— 本 bug 的修法。
// Windows 同时接受 "/" 与 "\"，所以这里用 filepath.Join 拼路径没有问题。
func windowsOpenSSHDirs() []string {
	root := strings.TrimSpace(getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows` // getenv 缺失时的系统默认值，避免拼出相对路径
	}
	dirs := []string{
		filepath.Join(root, "Sysnative", "OpenSSH"),
		filepath.Join(root, "System32", "OpenSSH"),
	}
	if pf := strings.TrimSpace(getenv("ProgramFiles")); pf != "" {
		dirs = append(dirs, filepath.Join(pf, "OpenSSH"))
	}
	return dirs
}

func envKeyFor(name string) string {
	if name == "sftp" {
		return envSFTPPath
	}
	return envSSHPath
}
