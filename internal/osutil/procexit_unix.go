//go:build !windows

package osutil

import (
	"errors"
	"os"
)

// processExitedOS 在 Unix 上不做预判：kill(2) 对已退出（僵尸）的子进程返回成功，
// 对已被 Wait 回收的进程由 os.Process.Kill 返回 os.ErrProcessDone（见 killErrorIsGone）。
// 因此无需额外系统调用，保持原有的"总是尝试 Kill"路径。
func (p *Process) processExitedOS() bool { return false }

// killErrorIsGone 报告 Kill 的错误是否表示"进程已经不在了"。
func (p *Process) killErrorIsGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}
