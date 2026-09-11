package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "sync-abc.json")
	fp := Fingerprint{Host: "h", Kind: "dir", RemoteRoot: "/r", LocalRoot: "/l", MaxDepth: 1, Excludes: []string{".git/"}}
	s := NewStateStore(path, fp)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	s.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 1, RemoteMTime: "t", LocalSize: 1, LocalMTime: "t", HasLocal: true}
	})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// 权限必须是 0600（状态含远端/本地路径等环境信息）
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("状态文件权限应为 0600，得到 %v", st.Mode().Perm())
	}

	s2 := NewStateStore(path, fp)
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	s2.With(func(d *StateFile) {
		if got := d.Entries["a.txt"]; got == nil || !got.HasLocal {
			t.Fatalf("round-trip 丢失条目: %#v", d.Entries)
		}
	})
}

// fingerprint 不匹配（改了 local_path / kind / host）⇒ 丢弃旧状态，按全量重扫处理。
func TestStateStoreDiscardsOnFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-abc.json")
	fpA := Fingerprint{Host: "h", LocalRoot: "/l1"}
	sa := NewStateStore(path, fpA)
	_ = sa.Load()
	sa.With(func(d *StateFile) { d.Entries["a.txt"] = &Entry{HasLocal: true, LocalSize: 1} })
	if err := sa.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	fpB := Fingerprint{Host: "h", LocalRoot: "/l2"} // 本地根变了
	sb := NewStateStore(path, fpB)
	if err := sb.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	sb.With(func(d *StateFile) {
		if len(d.Entries) != 0 {
			t.Fatalf("fingerprint 不匹配必须丢弃旧状态，得到 %#v", d.Entries)
		}
	})
}

// 损坏的状态文件不得让规则启动失败：只能退化为"下次全量重扫"。
func TestStateStoreCorruptFileDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-abc.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStateStore(path, Fingerprint{Host: "h"})
	if err := s.Load(); err != nil {
		t.Fatalf("损坏状态必须优雅降级，得到 %v", err)
	}
	s.With(func(d *StateFile) {
		if d.Entries == nil {
			t.Fatal("应初始化空 Entries")
		}
	})
}
