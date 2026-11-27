package forward

import (
	"sync"
	"testing"
	"time"

	"sshore/internal/osutil"
)

// F1 回归：首启 Start 的 trySpawn 窗口（释放 c.mu 期间）用户 Stop——
// proc 尚为 nil 无可杀，Stop 只关 cancel 并置 stopped；trySpawn 返回后
// Start 必须过 post-spawn 终检闸门（与 respawnLoop 同形），Kill 刚 spawn
// 的进程并保持 stopped，绝不无条件升级 connected 让已停止的隧道复活。
// 注入手法与 TestRespawnNoResurrectionAfterStopDuringSpawn 相同。
func TestStartNoResurrectionAfterStopDuringSpawn(t *testing.T) {
	var mu sync.Mutex
	var spawnedProc *osutil.Process
	stopCalled := false
	ctrl := NewCtrl(&fakeSpawner{}, func(e Event) {}, nil)
	sp := &fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		p, err := aliveProcess(t)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		spawnedProc = p
		mu.Unlock()
		mu.Lock()
		if !stopCalled {
			stopCalled = true
			mu.Unlock()
			// spawn 进行中触发 Stop：此刻 entry.proc==nil，Stop 关 cancel、置 stopped。
			_ = ctrl.Stop("s1")
		} else {
			mu.Unlock()
		}
		return p, nil
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "s1"
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 给可能错误的“复活写入”留出暴露时间
	time.Sleep(100 * time.Millisecond)
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("stopped tunnel must not resurrect after first start, got %s", got)
	}
	// Kill 子句：闸门 adopt 失败的新进程必须已被 Kill（Wait 短时间内返回）。
	mu.Lock()
	p := spawnedProc
	mu.Unlock()
	if p == nil {
		t.Fatal("spawn never happened")
	}
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process spawned during stop was not killed (ssh leak)")
	}
}

// F1 回归第二面：同一窗口内 OnShutdown 接管（关 cancel、置 stopped），
// Start 同样不得复活隧道，且必须杀掉刚 spawn 的进程避免退出时泄漏。
func TestStartNoResurrectionAfterShutdownDuringSpawn(t *testing.T) {
	var mu sync.Mutex
	var spawnedProc *osutil.Process
	shutdownCalled := false
	ctrl := NewCtrl(&fakeSpawner{}, func(e Event) {}, nil)
	sp := &fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		p, err := aliveProcess(t)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		spawnedProc = p
		mu.Unlock()
		mu.Lock()
		if !shutdownCalled {
			shutdownCalled = true
			mu.Unlock()
			ctrl.OnShutdown()
		} else {
			mu.Unlock()
		}
		return p, nil
	}}
	ctrl.spawner = sp

	tr := base()
	tr.ID = "s2"
	tr.ListenPort = freePort(t)
	if err := ctrl.Start(tr); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := ctrl.State(tr.ID); got != StateStopped {
		t.Fatalf("shutdown tunnel must not resurrect after first start, got %s", got)
	}
	mu.Lock()
	p := spawnedProc
	mu.Unlock()
	if p == nil {
		t.Fatal("spawn never happened")
	}
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process spawned during shutdown was not killed (ssh leak)")
	}
}
