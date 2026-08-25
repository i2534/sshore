//go:build !windows

package osutil

import "os/exec"

// On non-Windows platforms console subprocesses don't spawn a window, so this
// is a no-op.
func procAttrHideConsole(cmd *exec.Cmd) {}
