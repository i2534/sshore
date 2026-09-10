package sync

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Transferer 是 sync 对传输层的依赖抽象。生产实现是 sftp.Ctrl.Get 的适配器
// （internal/sync 不直接依赖 sftp，便于引擎测试完全脱离网络）。
type Transferer interface {
	Get(host, user, remote, local string) error
}

// PartSuffix 返回本规则的临时文件中缀。**必须含规则 id**：同一进程内所有规则
// 与 UI 触发的传输共享同一个 PID，只靠 pid 命名会让两条规则写同一个临时文件，
// 内容交错后 rename，静默损坏本地文件。
func PartSuffix(ruleID string) string {
	id := ruleID
	if len(id) > 8 {
		id = id[:8]
	}
	return ".sshore-part-" + id + "-"
}

// TransferTo 把远端文件原子地搬到 target：先写同目录的临时文件，成功后 rename。
// 中断/失败都不会污染目标文件。
func TransferTo(tr Transferer, host, user, remotePath, target, ruleID string, randSuffix func() string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if randSuffix == nil {
		// 默认必须是**每次调用都不同**的随机串：进程 pid 在整个应用生命周期内
		// 恒定，同一规则的两次传输（引擎一次、UI 触发一次）会写同一个临时文件。
		// randSuffix 参数只用于测试注入确定性后缀。
		randSuffix = func() string {
			var b [8]byte
			_, _ = rand.Read(b[:])
			return hex.EncodeToString(b[:])
		}
	}
	tmp := target + PartSuffix(ruleID) + randSuffix()
	if err := tr.Get(host, user, remotePath, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// CleanupParts 清理某规则在 localRoot 下遗留的临时文件，返回清理数量。
// 规则启动与删除时都要调用——否则崩溃或 Stop 之后它们会永远堆积。
func CleanupParts(localRoot, ruleID string) (int, error) {
	suffix := PartSuffix(ruleID)
	n := 0
	err := filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 不可读的目录跳过，不阻断清理
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), suffix) {
			if rmErr := os.Remove(path); rmErr == nil {
				n++
			}
		}
		return nil
	})
	return n, err
}
