//go:build windows

package osutil

import "syscall"

func sigint() syscall.Signal {
	return syscall.SIGTERM
}
