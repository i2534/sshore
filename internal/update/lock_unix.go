//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Acquire 在 exeDir 上取排他锁（flock LOCK_EX|LOCK_NB）。
//
// 返回非 nil 的 release 时加锁成功；release 幂等，重复调用安全。
// 进程退出（含被 kill）时内核会自动释放锁。
func Acquire(exeDir string) (func(), error) {
	path := filepath.Join(exeDir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		// 竞争失败时不能顺手删掉锁文件：持锁方可能正拿着它，删除会引入
		// "新进程锁住新 inode、旧进程仍锁着旧 inode" 的双持锁漏洞。
		_ = f.Close()
		return nil, fmt.Errorf("另一个实例正在升级：%w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}
