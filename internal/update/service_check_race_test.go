package update

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// F4 回归（I-1 同形）：Check 的守卫判断与 setLocked(StateChecking) 必须在
// 同一临界区。旧实现先 Lock 检查后 Unlock，中间无锁读 Config，再 Lock 置
// checking——两个并发 Check 双双放行、各发一次请求，旧响应还会覆盖新状态。
//
// 确定性构造：Config 回调里放一个「双方到齐才放行」的屏障。
//   - 旧实现：Config 在守卫之后调用 → 屏障放行意味着两个调用都已通过守卫
//     （此刻状态仍是 idle），随后必然双双置 checking、双双发请求。
//   - 新实现：Config 在守卫之前调用 → 屏障放行后两者竞争同一临界区，
//     恰好一个置 checking，另一个被 checking 守卫短路，不发请求。
//
// Doer 阻塞到测试显式放行：保证计数断言时第一个检查仍在飞（状态 checking），
// 排除「第一个检查已跑完落 available、第二个因此合法放行」的调度干扰。
func TestConcurrentCheckOnlyOneRequest(t *testing.T) {
	var cfgCalls int32
	bothEntered := make(chan struct{})
	var calls int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Config = func() Settings {
			if atomic.AddInt32(&cfgCalls, 1) == 2 {
				close(bothEntered)
			}
			<-bothEntered
			return Settings{Auto: true, Interval: time.Hour}
		}
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			<-release
			return latestOKResponse("v0.7.0"), nil
		})
	})

	const n = 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = svc.Check(context.Background(), true)
		}()
	}
	close(start)
	// 等第一个请求真正到达，再给旧实现的第二个调用一个确定的暴露窗口。
	waitCond(t, func() bool { return atomic.LoadInt32(&calls) >= 1 }, 5*time.Second)
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发 Check 只应产生 1 次检查请求，实际 %d（守卫与置位不在同一临界区）", got)
	}
	unblock()
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发 Check 全程只应 1 次请求，实际 %d", got)
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("最终状态应为 available, got %s", got)
	}
}
