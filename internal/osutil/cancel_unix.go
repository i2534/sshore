//go:build !windows

package osutil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateAndCancel 让一次性子进程独处一个新进程组，并在 ctx 取消时杀掉整组。
//
// 只 Kill 直接子进程是不够的：`sh -c "sleep 30"` 会 fork 出孙进程 sleep，
// 孙进程继承了 stdout/stderr 管道的写端；即使 sh 被杀，cmd.Wait() 仍会阻塞到
// 孙进程退出（超时形同虚设，且进程泄漏）。负 PID 的 Kill 覆盖整个进程组。
func isolateAndCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// 负 PID 表示整个进程组。
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
