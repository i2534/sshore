//go:build windows

package osutil

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW (0x08000000) prevents the child console process (ssh.exe /
// sftp.exe) from creating a new console window, which would otherwise flash a
// blank window when starting a tunnel or running an sftp command.
const createNoWindow = 0x08000000

func procAttrHideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
