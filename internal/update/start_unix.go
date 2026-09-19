//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// setDetached 让脚本进入新会话，主进程退出后不受影响。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
