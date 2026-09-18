package sftp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// swapJournal 是 backup-swap 崩溃恢复日志（spec D8 / 决策 backup-swap）。
//
// 背景：远端的「.part → target」提交在 OpenSSH 上没有可覆盖的普通 rename，
// 只能用 backup-swap（target → bak，part → target）。journal 的 intent（target/bak/part
// 三元组）在**任何破坏性 rename 之前**落盘（C1 修复轮 1），覆盖两个崩溃窗口：
//   - intent 已写、target→bak 尚未执行：target 旧内容仍在其原名下，恢复只需清掉陈条条目；
//   - target→bak 已执行、part→target 尚未执行：target 名暂缺而旧内容在随机 bak 名里，
//     恢复侧必须从 journal 拿到 bak，才能回滚或补完（bak 名无法从 target 反推，
//     长路径退化名更是只保留目录）。
//
// 因此「先改名再登记」是不允许的：那会留下「target 名已消失、journal 仍空」的不可恢复窗口。
//
// 真相在磁盘（$dir/swap-entries.json），不是内存缓存：Begin/Done/Recover 每次都读改写，
// 因此「重启后新实例」能读到上一个进程留下的未完成项。写入用 tmp+rename 保证原子
// （半截 JSON 比没有 journal 更危险：会让恢复误判）。
//
// 并发（多实例注意）：mu 是**实例级**互斥，只序列化同一个 swapJournal 的读改写。
// 若两个进程/两个 swapJournal 实例共享同一状态目录，会互相丢更新（lost update）。
// 当前设计只有 app 装配出的**唯一** GoBackend 一个写者；Task 13 必须保持「单写者」，
// 否则要换成文件锁（flock/LockFileEx）。
type swapJournal struct {
	dir string
	mu  sync.Mutex
}

const swapJournalFile = "swap-entries.json"

// swapEntry 是一条未完成的 backup-swap。部分字段是恢复时定位残留文件所必需。
type swapEntry struct {
	Target string `json:"target"`
	Bak    string `json:"bak"`
	Part   string `json:"part"`
}

// swapEntriesFile 是落盘形状：{"entries":[{...}]}。
type swapEntriesFile struct {
	Entries []swapEntry `json:"entries"`
}

func newSwapJournal(dir string) *swapJournal { return &swapJournal{dir: dir} }

func (j *swapJournal) filePath() string { return filepath.Join(j.dir, swapJournalFile) }

// load 读回当前条目。文件不存在（或为空）视为没有任何未完成项，不报错。
func (j *swapJournal) load() ([]swapEntry, error) {
	b, err := os.ReadFile(j.filePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var f swapEntriesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Entries, nil
}

// store 原子写回。没有条目时直接删文件（Done 全部完成后不留垃圾）。
func (j *swapJournal) store(entries []swapEntry) error {
	if len(entries) == 0 {
		if err := os.Remove(j.filePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(j.dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(swapEntriesFile{Entries: entries})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(j.dir, swapJournalFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// 同目录 rename：Linux 与 Windows（MoveFileEx 带 REPLACE_EXISTING）都覆盖已存在目标。
	if err := os.Rename(tmpName, j.filePath()); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Begin 在**任何破坏性改名之前**登记一条 intent（target,bak,part）（C1 修复轮 1）。
// 调用方契约：Begin 返回错误时不得再执行 target→bak —— 否则会进入无 journal 保护的窗口。
// 同一 target 重复 Begin（上次失败/本次重试）替换旧条目，绝不累积重复 target。
func (j *swapJournal) Begin(target, bak, part string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := j.load()
	if err != nil {
		return err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Target != target {
			out = append(out, e)
		}
	}
	out = append(out, swapEntry{Target: target, Bak: bak, Part: part})
	return j.store(out)
}

// Done 在 part → target 已完成后移除该项。target 不存在时幂等成功。
func (j *swapJournal) Done(target string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := j.load()
	if err != nil {
		return err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Target != target {
			out = append(out, e)
		}
	}
	return j.store(out)
}

// Recover 返回所有未完成的 swap 条目（target+bak+part），供启动时恢复。
// 必须给出完整三元组：bak 是随机名，恢复侧无法从 target 反推，长路径退化名更只保留目录。
// 只读，不改动 journal 文件；读取失败或文件损坏时返回 nil —— 恢复是尽力而为，
// 绝不让坏文件阻断启动（损坏的 journal 会让这一条 swap 无法自动恢复，但不阻止其它操作）。
func (j *swapJournal) Recover() []swapEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := j.load()
	if err != nil {
		return nil
	}
	return entries
}
