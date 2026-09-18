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
	if err := j.Begin("h1", "u1", "/data/a.txt", "/data/a.txt.sshore-sftppart-bak-1", "/data/a.txt.sshore-sftppart-t1-2"); err != nil {
		t.Fatal(err)
	}
	got := j.Recover()
	// Recover 必须给出完整三元组：bak 是随机名，恢复侧无法从 target 反推（修复轮 1 M）。
	if len(got) != 1 || got[0].Target != "/data/a.txt" ||
		got[0].Bak != "/data/a.txt.sshore-sftppart-bak-1" || got[0].Part != "/data/a.txt.sshore-sftppart-t1-2" {
		t.Fatalf("恢复条目应含完整 target/bak/part，got %+v", got)
	}
	// 修复轮 1（F1）：归属（host/user）必须一并落盘并被 Recover 读回。
	if got[0].Host != "h1" || got[0].User != "u1" {
		t.Fatalf("恢复条目必须带 host/user，got host=%q user=%q", got[0].Host, got[0].User)
	}
	if err := j.Done("h1", "/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("完成后不应再报告，got %v", got)
	}
	// 幂等：done 不存在也算成功
	if err := j.Done("h1", "/data/none.txt"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "swap-entries.json"))
}

// TestJournalPersistsAcrossInstances：journal 的真相在磁盘文件（swap-entries.json），
// 新实例（模拟重启/重新装配）必须能读到上一个实例写下的未完成项；全部 Done 后文件被清掉。
func TestJournalPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("h1", "u1", "/data/a.txt", "/data/a.txt.bak", "/data/a.txt.part"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("h1", "u1", "/data/b.txt", "/data/b.txt.bak", "/data/b.txt.part"); err != nil {
		t.Fatal(err)
	}

	// 磁盘形状：{entries:[{host,user,target,bak,part}]}，字段名是 Task 13 恢复时的契约。
	raw, err := os.ReadFile(filepath.Join(dir, "swap-entries.json"))
	if err != nil {
		t.Fatalf("Begin 必须把条目落盘: %v", err)
	}
	var f struct {
		Entries []struct {
			Host   string `json:"host"`
			User   string `json:"user"`
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
	if f.Entries[0].Host != "h1" || f.Entries[0].User != "u1" ||
		f.Entries[0].Target != "/data/a.txt" || f.Entries[0].Bak != "/data/a.txt.bak" || f.Entries[0].Part != "/data/a.txt.part" {
		t.Fatalf("第一条字段不符: %+v", f.Entries[0])
	}

	// 模拟重启：新实例读同一目录。
	j2 := newSwapJournal(dir)
	got := j2.Recover()
	if len(got) != 2 || got[0].Target != "/data/a.txt" || got[1].Target != "/data/b.txt" {
		t.Fatalf("重启后应恢复出两条未完成目标，got %v", got)
	}

	// Done 只移除指定主机的指定目标，不影响其它条目。
	if err := j2.Done("h1", "/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := newSwapJournal(dir).Recover(); len(got) != 1 || got[0].Target != "/data/b.txt" {
		t.Fatalf("Done 后应只剩 b.txt，got %v", got)
	}
	if err := j2.Done("h1", "/data/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "swap-entries.json")); !os.IsNotExist(err) {
		t.Fatalf("全部完成后 journal 文件应被清掉，stat err=%v", err)
	}
}

// TestJournalBeginSameTargetReplaces：同一 (host,target) 再次 Begin（上一次已回滚/重试）
// 必须替换旧条目，不能累积重复 target —— 否则 Recover 会对同一目标重复恢复。
func TestJournalBeginSameTargetReplaces(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("h1", "u1", "/data/a.txt", "/data/a.txt.bak-1", "/data/a.txt.part-1"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("h1", "u1", "/data/a.txt", "/data/a.txt.bak-2", "/data/a.txt.part-2"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 1 || got[0].Target != "/data/a.txt" {
		t.Fatalf("同主机同目标应只有一条，got %v", got)
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

// TestJournalSameTargetDifferentHostsCoexist（F1）：不同主机上可能有同名 target，
// Begin 去重必须带 host —— 否则第二台主机的 Begin 会把第一台的条目顶掉，
// 那台机器上的中断现场再也无法恢复。
func TestJournalSameTargetDifferentHostsCoexist(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("hostA", "ua", "/data/a.txt", "/data/a.bak", "/data/a.part"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("hostB", "ub", "/data/a.txt", "/data/a.bak", "/data/a.part"); err != nil {
		t.Fatal(err)
	}
	got := j.Recover()
	if len(got) != 2 {
		t.Fatalf("不同主机的同名 target 必须各留一条，got %+v", got)
	}
	// Done 只删本主机那条。
	if err := j.Done("hostA", "/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	got = j.Recover()
	if len(got) != 1 || got[0].Host != "hostB" {
		t.Fatalf("Done(hostA) 不得误删 hostB 的条目，got %+v", got)
	}
}

// TestJournalReadsLegacyEntriesWithoutHost（F1 向后兼容）：升级前写入的条目没有
// host/user 字段，反序列化后必须得到空串（而不是报错或丢失其它字段），恢复侧据此
// 判定「归属未知」。JSON 里不得因为 omitempty 把空 host 写成多余字段。
func TestJournalReadsLegacyEntriesWithoutHost(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"entries":[{"target":"/old/a.txt","bak":"/old/a.bak","part":"/old/a.part"}]}`
	if err := os.WriteFile(filepath.Join(dir, "swap-entries.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	got := newSwapJournal(dir).Recover()
	if len(got) != 1 {
		t.Fatalf("旧条目必须能被读回: %+v", got)
	}
	if got[0].Host != "" || got[0].User != "" {
		t.Fatalf("旧条目的 host/user 必须是空串（归属未知），got %+v", got[0])
	}
	if got[0].Target != "/old/a.txt" || got[0].Bak != "/old/a.bak" || got[0].Part != "/old/a.part" {
		t.Fatalf("旧条目的 target/bak/part 必须原样保留，got %+v", got[0])
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
	if err := j.Begin("h1", "u1", "/data/a.txt", "bak", "part"); err != nil {
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
