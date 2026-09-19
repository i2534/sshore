package sftp

import (
	"io"
	"strings"
	"testing"
	"time"
)

// 技术审核 R13/评审 M7：ReadFrom 对 EOF 返回 (n, nil)，所以「截断的源」不会报错 ——
// 必须靠 done!=total 兜住；这里用 copyStream + 截断 reader 直接钉住这条防线。
func TestTruncatedSourceIsCaughtByByteCount(t *testing.T) {
	total := int64(100)
	got, err := copyStream(io.Discard, io.LimitReader(strings.NewReader(strings.Repeat("x", 100)), 40))
	if err != nil {
		t.Fatalf("截断读取本身不该报错（这正是危险之处）: %v", err)
	}
	if got != 40 {
		t.Fatalf("copyStream 应返回真实搬运字节数，got %d", got)
	}
	if decideCommit(got, total) != commitShortRead {
		t.Fatalf("少传 %d/%d 必须拒绝提交", got, total)
	}
}

func TestDecideCommitRequiresExactTotal(t *testing.T) {
	if decideCommit(100, 100) != commitOK {
		t.Fatal("done==total 才允许提交")
	}
	if decideCommit(99, 100) != commitShortRead {
		t.Fatal("少一字节也必须拒绝提交（ReadFrom 对 EOF 返回 nil，spec R13）")
	}
	// 多传（本地 .part 预置了多余字节/远端被改写）同样不是「完整」：必须拒绝提交。
	if decideCommit(101, 100) != commitShortRead {
		t.Fatal("done>total 也不得提交，只有精确相等才是完整")
	}
	if decideCommit(0, 0) != commitOK {
		t.Fatal("0 字节文件（done==total==0）应允许提交")
	}
}

func TestProgressEmitterThrottlesButForcesFinalFrame(t *testing.T) {
	now := time.Unix(0, 0)
	sent := 0
	e := newProgressEmitter("t1", func(Progress) { sent++ })
	e.now = func() time.Time { return now }

	e.send(Progress{Done: 1}, false)
	e.send(Progress{Done: 2}, false) // 200ms 内被节流
	if sent != 1 {
		t.Fatalf("节流失效，sent=%d", sent)
	}
	now = now.Add(250 * time.Millisecond)
	e.send(Progress{Done: 3}, false)
	if sent != 2 {
		t.Fatalf("超过节流窗口应放行，sent=%d", sent)
	}
	e.send(Progress{Done: 4}, true) // 末帧强制
	if sent != 3 {
		t.Fatalf("末帧必须强制发送，sent=%d", sent)
	}
}

// TestScanLimitReachedAndDegradedProgress（Task 12 Step 1）：把 D16 的两个阈值与降级帧
// 字段闭环钉死在纯函数上 —— 19999 个文件/1s 不降级；20001 个文件或 6s 必须降级；
// 降级帧必须是 Total=-1 && FilesTotal=-1 && Phase=transfer（UI 据此走不定进度）。
func TestScanLimitReachedAndDegradedProgress(t *testing.T) {
	if scanLimitReached(19999, 1*time.Second) {
		t.Fatal("未到阈值不应降级")
	}
	if !scanLimitReached(20001, 1*time.Second) {
		t.Fatal("超过 20000 文件必须降级")
	}
	if !scanLimitReached(10, 6*time.Second) {
		t.Fatal("超过 5s 必须降级")
	}
	p := degradedProgress("t1", "h", DirDownload, "d")
	if p.Total != -1 || p.FilesTotal != -1 || p.Phase != PhaseTransfer {
		t.Fatalf("降级后必须是 Total=-1/FilesTotal=-1/Phase=transfer, got %#v", p)
	}
}

// TestProgressEmitterStampsIDAndToleratesNilReport：ID 由发射器统一盖戳
// （调用方不必逐个填），nil report 不得 panic（legacy 面/未接线时）。
func TestProgressEmitterStampsIDAndToleratesNilReport(t *testing.T) {
	var got []Progress
	e := newProgressEmitter("id-9", func(p Progress) { got = append(got, p) })
	e.send(Progress{Done: 1, Phase: PhaseTransfer}, true)
	e.send(Progress{Done: 2, Phase: PhaseTransfer}, true)
	if len(got) != 2 {
		t.Fatalf("强制帧必须都发出，got %d", len(got))
	}
	for i, p := range got {
		if p.ID != "id-9" {
			t.Fatalf("第 %d 帧 ID 未被盖戳: %q", i, p.ID)
		}
	}
	// nil report 不得 panic。
	newProgressEmitter("x", nil).send(Progress{Done: 1}, true)
}
