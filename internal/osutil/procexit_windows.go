//go:build windows

package osutil

import (
	"errors"
	"os"
	"syscall"
)

// stillActive 是 GetExitCodeProcess 对"仍在运行"的进程返回的哨兵值。
// Win32 的 STILL_ACTIVE（0x103=259）在标准库 syscall 里没有导出常量，按其取值。
const stillActive = 259

// processExitedOS 查询直接子进程是否已经结束。
//
// 判定"已退出"不能只看 TerminateProcess 的错误：Windows 对**已经退出**的进程调
// TerminateProcess 会返回 ERROR_ACCESS_DENIED，与"权限不足"是同一个错误码，
// 只有 GetExitCodeProcess 能区分。子进程句柄由 os/exec 持有，只要还没 Wait 就有效，
// 所以用 os.Process.WithHandle 在真实句柄上查退出码（而不是按 PID 重新 OpenProcess，
// 避免 PID 复用）。
func (p *Process) processExitedOS() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return true
	}
	var code uint32
	queried := false
	err := p.cmd.Process.WithHandle(func(handle uintptr) {
		queried = syscall.GetExitCodeProcess(syscall.Handle(handle), &code) == nil
	})
	if err != nil {
		// 句柄已被 Wait/Release 回收（os.ErrProcessDone / os: process already released）
		// ——进程已经结束，没有可杀对象。
		return true
	}
	if !queried {
		// 查询失败：不吞错，保守当作仍在运行，让 Kill 走正常路径并如实报错。
		return false
	}
	return code != stillActive
}

// killErrorIsGone 报告 Kill 的错误是否表示"进程已经退出"。
//
// ERROR_ACCESS_DENIED（TerminateProcess 对已退出进程）与 EINVAL（句柄已被 Wait 回收后
// Go 在 statusReleased 分支返回，见 os/exec_windows.go）都只是候选信号，必须回到句柄上
// 用退出码确认；真正的权限不足查到的仍是 STILL_ACTIVE，不会被当成成功吞掉。
func (p *Process) killErrorIsGone(err error) bool {
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	if errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.EINVAL) {
		return p.processExitedOS()
	}
	return false
}
