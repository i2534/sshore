//go:build windows

package osutil

import "os/exec"

// Windows 无进程组语义，且 SysProcAttr 已被 procAttrHideConsole 占用
// （CREATE_NO_WINDOW）；沿用 exec.CommandContext 默认的 Cancel（直接 Kill）。
func isolateAndCancel(cmd *exec.Cmd) {}
