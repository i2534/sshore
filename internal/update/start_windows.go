//go:build windows

package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setDetached 隐藏控制台窗口；子进程不随父进程退出（见 spec §8.5）。
// 常量取自 golang.org/x/sys/windows —— syscall 包没有 CREATE_NO_WINDOW（spec §4 F7）。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
