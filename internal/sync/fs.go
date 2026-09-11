package sync

import (
	"os"
	"time"
)

// FormatModTime 是本地文件时间的**唯一**序列化格式。
// 引擎记录 local_mtime 与 UI 读取 LocalState 都必须用它：两处格式不一致会让
// "未被改动"被误判成"被改动"，进而刷出假冲突。
func FormatModTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func osStat(p string) (LocalState, error) {
	st, err := os.Stat(p)
	if err != nil {
		return LocalState{}, err
	}
	return LocalState{Exists: true, Size: st.Size(), ModTime: FormatModTime(st.ModTime())}, nil
}

func osRemove(p string) error { return os.Remove(p) }
