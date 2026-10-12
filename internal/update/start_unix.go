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

// StartDetached 分离启动脚本：execve 不经 shell，argv 天然零插值；脚本必须在主进程
// 退出后继续跑，所以 setsid 脱离会话且不 Wait。
func StartDetached(_ string, scriptPath string, args, _ []string) error {
	cmd := exec.Command(scriptPath, args...)
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
