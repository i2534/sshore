package sftp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestPoolGlobalTokenSerializesAcrossUsers 是 Task 17 修复波 / I1 的诚实化钉子。
//
// acquireInflight 的去重键含 user（M3），注释曾声称「不同 user 对同一 host+目标是两条
// 互不相干的传输」。这句话在**去重表**这一层成立，但在生产整体上不成立：真正把传输
// 变串行的是池的**全局单传输额度**（AcquireTransfer 的容量 1 queue），它只看额度、
// 完全不看 host/user/目标 —— 所以跨 user 的传输照样必须一个等一个。
//
// 本用例用同一个池同时钉住两件事：
//  1. 额度被占用时，第二个 AcquireTransfer（无论同 user 还是不同 user）都拿不到会话、
//     不会拨号；
//  2. 归还额度后第二个立刻拿到会话。
//
// 它同时说明 I1 说的「跨 user 去重语义在生产里不可达」：不同 user 的第二次触发根本
// 走不到「与第一条并发」的状态，它先被全局额度挡住。
func TestPoolGlobalTokenSerializesAcrossUsers(t *testing.T) {
	var dials int32
	p := NewPool(func(host, user string) (*Session, error) {
		atomic.AddInt32(&dials, 1)
		return &Session{Host: host, User: user}, nil
	})
	defer p.CloseAll()

	held, err := p.AcquireTransfer(context.Background(), "h", "alice")
	if err != nil {
		t.Fatal(err)
	}

	// 不同 user：拿不到额度，且不得拨号。
	doneBob := make(chan error, 1)
	go func() {
		s, aerr := p.AcquireTransfer(context.Background(), "h", "bob")
		if aerr == nil {
			p.Release(s, false)
		}
		doneBob <- aerr
	}()
	// 同 user：同样拿不到额度。
	doneAlice := make(chan error, 1)
	go func() {
		s, aerr := p.AcquireTransfer(context.Background(), "h", "alice")
		if aerr == nil {
			p.Release(s, false)
		}
		doneAlice <- aerr
	}()

	select {
	case err := <-doneBob:
		t.Fatalf("额度被占时不同 user 也必须排队（不得提前返回）: %v", err)
	case err := <-doneAlice:
		t.Fatalf("额度被占时同 user 也必须排队（不得提前返回）: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if n := atomic.LoadInt32(&dials); n != 1 {
		t.Fatalf("额度被占时不得再拨号（dials=%d，want 1）：全局单传输串行被破坏", n)
	}

	p.Release(held, false)
	for _, ch := range []chan error{doneBob, doneAlice} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("归还额度后排队者必须成功: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("归还额度后 5s 内排队者未拿到会话")
		}
	}
	if n := atomic.LoadInt32(&dials); n != 3 {
		t.Fatalf("三条传输各拨号一次（dials=%d，want 3）", n)
	}
}

// TestGoBackendDedupRejectsSameTargetBeforeTakingToken（Task 17 修复波 / I1）：
// 去重表必须**先于**全局传输额度生效 —— 同 host+user+方向+目标的第二次触发被立即拒绝，
// 不会去拨号（dials 不变），也不会在额度队列里静默排队（排队后第一条完成后第二条照样覆盖，
// 用户看不到任何提示：这正是 Get 注释里「绝不排队」的落地）。
func TestGoBackendDedupRejectsSameTargetBeforeTakingToken(t *testing.T) {
	var dials int32
	g := NewGoBackend(nil, nil)
	g.pool.dial = func(host, user string) (*Session, error) {
		atomic.AddInt32(&dials, 1)
		return &Session{Host: host, User: user}, nil
	}
	defer g.CloseAll()

	release, err := g.acquireInflight("h", "alice", string(DirDownload), "/local/dst.bin", "first")
	if err != nil {
		t.Fatalf("第一条不应被拒: %v", err)
	}
	// 同 user 同目标：立即被拒（且不是「排队等 token」—— 这里根本没取额度）。
	errDup := g.Get(TransferRequest{ID: "second", Host: "h", User: "alice", Remote: "src.bin", Local: "/local/dst.bin", Atomic: true}, nil)
	if errDup == nil {
		t.Fatal("同 user 同目标的第二次触发必须被立即拒绝（去重表先于额度生效）")
	}
	if !strings.Contains(errDup.Error(), "同一目标已在传输中") {
		t.Fatalf("去重拒绝必须是「同一目标已在传输中」形状，got %v", errDup)
	}
	if n := atomic.LoadInt32(&dials); n != 0 {
		t.Fatalf("被去重拒绝的触发绝不应拨号（dials=%d）", n)
	}
	release()
	// 释放后同目标可再次触发（不会永久锁死）。
	rel2, err := g.acquireInflight("h", "alice", string(DirDownload), "/local/dst.bin", "third")
	if err != nil {
		t.Fatalf("释放后同一目标必须能再次触发: %v", err)
	}
	rel2()
}
