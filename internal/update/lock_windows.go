//go:build windows

package update

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Acquire 在 exeDir 上取排他锁（命名互斥体）。
//
// 返回非 nil 的 release 时加锁成功；release 幂等，重复调用安全。
// 互斥体句柄随进程关闭，进程退出时锁自动释放，脚本不需要参与。
func Acquire(exeDir string) (func(), error) {
	sum := sha1.Sum([]byte(strings.ToLower(exeDir)))
	name, err := windows.UTF16PtrFromString("Global\\sshore-update-" + hex.EncodeToString(sum[:8]))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	// GetLastError 必须紧跟 CreateMutex 取，中间不能插入任何可能覆写
	// 线程 last-error 的调用；命名互斥体已存在时返回 ERROR_ALREADY_EXISTS。
	lastErr := windows.GetLastError()
	if h != 0 && (err != nil || errors.Is(lastErr, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)) {
		// 两种冲突路径都要关掉 CreateMutex 返回的句柄，否则句柄外泄、
		// 进程存活期间会一直把自己挡在锁外。
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("另一个实例正在升级")
	}
	if err != nil {
		return nil, fmt.Errorf("创建互斥体失败：%w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = windows.CloseHandle(h) })
	}, nil
}
