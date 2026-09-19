package update

import (
	_ "embed"
	"fmt"
	"os/exec"
	"strconv"
)

//go:embed scripts/update.sh
var scriptSH []byte

//go:embed scripts/update.cmd
var scriptCMD []byte

// ScriptName 返回该平台的脚本文件名。
func ScriptName(goos string) string {
	if goos == "windows" {
		return "sshore-update.cmd"
	}
	return "sshore-update.sh"
}

// ScriptBytes 返回嵌入的脚本内容（按 GOOS 选择）。
func ScriptBytes(goos string) ([]byte, error) {
	if goos == "windows" {
		if len(scriptCMD) == 0 {
			return nil, fmt.Errorf("未嵌入 update.cmd")
		}
		return scriptCMD, nil
	}
	if len(scriptSH) == 0 {
		return nil, fmt.Errorf("未嵌入 update.sh")
	}
	return scriptSH, nil
}

// ScriptArgs 生成 Linux 侧 argv（execve 不经 shell，天然零插值）。
func ScriptArgs(p Plan, pid int) []string {
	return []string{
		"--pid", strconv.Itoa(pid),
		"--target", p.Target,
		"--pending", p.Pending,
		"--backup", p.Backup,
		"--size", strconv.FormatInt(p.Size, 10),
		"--log", p.LogPath,
		"--wait", strconv.Itoa(int(p.Wait / 1e9)),
	}
}

// ScriptEnv 生成 Windows 侧环境变量（argv 经 cmd 转发不满足零插值，见 spec §8.5）。
func ScriptEnv(p Plan, pid int) []string {
	return []string{
		"SSHORE_PID=" + strconv.Itoa(pid),
		"SSHORE_TARGET=" + p.Target,
		"SSHORE_PENDING=" + p.Pending,
		"SSHORE_BACKUP=" + p.Backup,
		"SSHORE_SIZE=" + strconv.FormatInt(p.Size, 10),
		"SSHORE_LOG=" + p.LogPath,
		"SSHORE_WAIT=" + strconv.Itoa(int(p.Wait/1e9)),
	}
}

// StartDetached 分离启动脚本：两侧都不依赖父进程存活。
func StartDetached(goos, scriptPath string, args, env []string) error {
	var cmd *exec.Cmd
	if goos == "windows" {
		cmd = exec.Command("cmd", "/d", "/c", scriptPath)
		cmd.Env = append(cmd.Environ(), env...)
	} else {
		cmd = exec.Command(scriptPath, args...)
	}
	setDetached(cmd) // 平台差异（新会话 / 隐藏窗口）见 start_unix.go 与 start_windows.go
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait：脚本要在本进程退出后继续跑。
	return cmd.Process.Release()
}
