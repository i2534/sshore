package sftp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJournalBeginDoneRecover 是 brief 的逐字用例：Begin 后 Recover 报告未完成目标，
// Done 后不再报告，且 Done 对不存在的目标幂等。
func TestJournalBeginDoneRecover(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("/data/a.txt", "/data/a.txt.sshore-sftppart-bak-1", "/data/a.txt.sshore-sftppart-t1-2"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("恢复列表应含未完成的目标，got %v", got)
	}
	if err := j.Done("/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("完成后不应再报告，got %v", got)
	}
	// 幂等：done 不存在也算成功
	if err := j.Done("/data/none.txt"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "swap-entries.json"))
}

// TestJournalPersistsAcrossInstances：journal 的真相在磁盘文件（swap-entries.json），
// 新实例（模拟重启/重新装配）必须能读到上一个实例写下的未完成项；全部 Done 后文件被清掉。
func TestJournalPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("/data/a.txt", "/data/a.txt.bak", "/data/a.txt.part"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("/data/b.txt", "/data/b.txt.bak", "/data/b.txt.part"); err != nil {
		t.Fatal(err)
	}

	// 磁盘形状：{entries:[{target,bak,part}]}，字段名是 Task 13 恢复时的契约。
	raw, err := os.ReadFile(filepath.Join(dir, "swap-entries.json"))
	if err != nil {
		t.Fatalf("Begin 必须把条目落盘: %v", err)
	}
	var f struct {
		Entries []struct {
			Target string `json:"target"`
			Bak    string `json:"bak"`
			Part   string `json:"part"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("journal 不是合法 JSON: %v (%s)", err, raw)
	}
	if len(f.Entries) != 2 {
		t.Fatalf("应有 2 条，got %+v", f.Entries)
	}
	if f.Entries[0].Target != "/data/a.txt" || f.Entries[0].Bak != "/data/a.txt.bak" || f.Entries[0].Part != "/data/a.txt.part" {
		t.Fatalf("第一条字段不符: %+v", f.Entries[0])
	}

	// 模拟重启：新实例读同一目录。
	j2 := newSwapJournal(dir)
	got := j2.Recover()
	if len(got) != 2 || got[0] != "/data/a.txt" || got[1] != "/data/b.txt" {
		t.Fatalf("重启后应恢复出两条未完成目标，got %v", got)
	}

	// Done 只移除指定目标，不影响其它条目。
	if err := j2.Done("/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := newSwapJournal(dir).Recover(); len(got) != 1 || got[0] != "/data/b.txt" {
		t.Fatalf("Done 后应只剩 b.txt，got %v", got)
	}
	if err := j2.Done("/data/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "swap-entries.json")); !os.IsNotExist(err) {
		t.Fatalf("全部完成后 journal 文件应被清掉，stat err=%v", err)
	}
}

// TestJournalBeginSameTargetReplaces：同一目标再次 Begin（上一次已回滚/重试）必须替换旧条目，
// 不能累积重复 target —— 否则 Recover 会对同一目标重复恢复。
func TestJournalBeginSameTargetReplaces(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("/data/a.txt", "/data/a.txt.bak-1", "/data/a.txt.part-1"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("/data/a.txt", "/data/a.txt.bak-2", "/data/a.txt.part-2"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("同目标应只有一条，got %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "swap-entries.json"))
	if err != nil {
		t.Fatal(err)
	}
	sfx := string(raw)
	if !strings.Contains(sfx, "/data/a.txt.bak-2") || !strings.Contains(sfx, "/data/a.txt.part-2") ||
		strings.Contains(sfx, "bak-1") || strings.Contains(sfx, "part-1") {
		t.Fatalf("Begin 应替换旧条目，got %s", sfx)
	}
}

// TestJournalRecoverMissingFileIsEmpty：从未写过 journal 时 Recover 必须返回空（不是 panic）。
func TestJournalRecoverMissingFileIsEmpty(t *testing.T) {
	j := newSwapJournal(t.TempDir())
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("无 journal 文件时应为空，got %v", got)
	}
}

// TestJournalDoesNotLeaveTempFiles：原子写（tmp+rename）结束后不得留下中间临时文件。
func TestJournalDoesNotLeaveTempFiles(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("/data/a.txt", "bak", "part"); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != "swap-entries.json" {
			t.Fatalf("journal 目录只应有 swap-entries.json，got %s", e.Name())
		}
	}
}
