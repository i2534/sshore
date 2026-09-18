package sftp

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

// sessionState 是会话状态机的状态（spec D2）。
type sessionState int

const (
	sessIdle    sessionState = iota // 可被复用
	sessBusy                        // 被一个传输/列表操作独占
	sessClosing                     // 已发起关闭，等子进程退出
	sessDead                        // 已关闭
)

const (
	probeTimeout   = 5 * time.Second
	shutdownGrace  = 5 * time.Second
	maxIdlePerHost = 2
)

// Session 是一条 ssh -s sftp 长驻会话。
type Session struct {
	Host string
	User string
	Conn *sftp.Client
	Proc *osutil.PipedProcess

	state    sessionState
	last     time.Time
	transfer bool
	mu       sync.Mutex
}

// Closed 报告会话是否已进入 closing/dead（Release、Probe 据此判定能否复用）。
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == sessDead || s.state == sessClosing
}

// setState 是唯一写 state 的入口（技术审核 S7：Release/AcquireList 里裸写 state
// 与 Closed/close 构成数据竞争，-race 已复现）。
func (s *Session) setState(st sessionState) {
	s.mu.Lock()
	s.state = st
	s.last = time.Now()
	s.mu.Unlock()
}

// markTransfer 标记该会话占用了传输额度（技术审核 S8：列表会话 Release 不得归还
// 额度，否则并发 1 被突破）。
func (s *Session) markTransfer() {
	s.mu.Lock()
	s.transfer = true
	s.mu.Unlock()
}

func (s *Session) isTransfer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer
}

// close 走有界退出流程：关 client → 关管道 → 有界等待 → 必要时 Kill。
// 任何 goroutine 都不得在持有 Pool.mu 时调用它（实现里总是在放锁之后调用）。
func (s *Session) close() {
	s.setState(sessClosing)
	if s.Conn != nil {
		_ = s.Conn.Close()
	}
	if s.Proc != nil {
		_ = s.Proc.Close()
		done := make(chan struct{})
		go func() {
			_ = s.Proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownGrace):
			_ = s.Proc.Kill()
		}
	}
	s.setState(sessDead)
}

// DialFunc 由 GoBackend 提供（起 ssh -s sftp + NewClientPipe）。
type DialFunc func(host, user string) (*Session, error)

// Pool 是 (host,user) → 会话池。传输并发上限 1（FIFO 排队）；idle 上限 2/键（LRU）。
type Pool struct {
	mu        sync.Mutex
	dial      DialFunc
	idle      map[string][]*Session
	transfers int
	queue     chan struct{} // 传输并发额度（容量 1）
	all       map[*Session]struct{}
}

func NewPool(d DialFunc) *Pool {
	q := make(chan struct{}, 1)
	q <- struct{}{}
	return &Pool{dial: d, idle: map[string][]*Session{}, queue: q, all: map[*Session]struct{}{}}
}

func (p *Pool) track(s *Session) *Session {
	p.mu.Lock()
	p.all[s] = struct{}{}
	p.mu.Unlock()
	return s
}

// AcquireTransfer：传输并发上限 1，超出 FIFO 排队；每个传输独占新建会话。
func (p *Pool) AcquireTransfer(ctx context.Context, host, user string) (*Session, error) {
	select {
	case <-p.queue:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s, err := p.dial(host, user)
	if err != nil {
		p.queue <- struct{}{}
		return nil, err
	}
	// 技术审核 S9：拿到额度后 ctx 可能已取消 —— 此时必须归还额度并关掉刚建的
	// 会话，否则 token 与会话双泄漏，后续传输永久阻塞。
	if cerr := ctx.Err(); cerr != nil {
		s.close()
		p.queue <- struct{}{}
		return nil, cerr
	}
	s.markTransfer()
	s.setState(sessBusy)
	return p.track(s), nil
}

// AcquireList：优先复用 idle；池空则新建；超出 idle 上限关最久未用者。
// 绝不排队、绝不因池满失败（spec D2）。
// 注意：调用方一律传 context.Background()，不要传 nil（Probe 会对 ctx 做 WithTimeout）。
func (p *Pool) AcquireList(ctx context.Context, host, user string) (*Session, error) {
	key := host + "\x00" + user
	p.mu.Lock()
	if lst := p.idle[key]; len(lst) > 0 {
		s := lst[len(lst)-1]
		p.idle[key] = lst[:len(lst)-1]
		s.setState(sessBusy)
		p.mu.Unlock()
		return s, nil
	}
	p.mu.Unlock()
	s, err := p.dial(host, user)
	if err != nil {
		return nil, err
	}
	s.setState(sessBusy)
	return p.track(s), nil
}

// Release：reusable=true 时把会话放回 idle（并按上限 LRU 关闭多余），否则直接关。
func (p *Pool) Release(s *Session, reusable bool) {
	if s == nil {
		return
	}
	// 只有传输会话才归还并发额度；列表会话从不占用额度（技术审核 S8）。
	defer func() {
		if s.isTransfer() {
			p.refill()
		}
	}()
	if !reusable || s.Closed() {
		p.mu.Lock()
		delete(p.all, s)
		p.mu.Unlock()
		s.close()
		return
	}
	key := s.Host + "\x00" + s.User
	s.setState(sessIdle)
	p.mu.Lock()
	p.idle[key] = append(p.idle[key], s)
	var evict []*Session
	if len(p.idle[key]) > maxIdlePerHost {
		evict = append(evict, p.idle[key][0])
		p.idle[key] = p.idle[key][1:]
	}
	for e := range evict {
		delete(p.all, evict[e])
	}
	p.mu.Unlock()
	for _, e := range evict {
		e.close()
	}
}

// refill 归还一个传输并发额度（只由传输会话的 Release 触发；列表会话不占额度）。
func (p *Pool) refill() {
	select {
	case p.queue <- struct{}{}:
	default:
	}
}

// Probe 用库唯一 ctx 感知的 ReadDirContext 探活；超时/取消即关掉并丢弃该会话。
func (p *Pool) Probe(ctx context.Context, host, user string) bool {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// ctx 已取消/超时：不新建也不复用（不得在取消后动池）。
	if cctx.Err() != nil {
		return false
	}
	s, err := p.AcquireList(cctx, host, user)
	if err != nil {
		return false
	}
	// 测试替身/异常会话没有连接：确定性失败，绝不 panic；失败会话一律丢弃。
	if s.Conn == nil {
		p.Release(s, false)
		return false
	}
	_, err = s.Conn.ReadDirContext(cctx, ".")
	p.Release(s, err == nil)
	return err == nil
}

// Disconnect 只关闭该 host 的空闲会话；进行中的传输不受影响。
func (p *Pool) Disconnect(host string) error {
	p.mu.Lock()
	var drop []*Session
	for key, lst := range p.idle {
		if len(key) >= len(host) && key[:len(host)] == host {
			drop = append(drop, lst...)
			delete(p.idle, key)
		}
	}
	for _, s := range drop {
		delete(p.all, s)
	}
	p.mu.Unlock()
	for _, s := range drop {
		s.close()
	}
	return nil
}

// CloseAll 关闭所有已知会话（含进行中的传输），供 OnShutdown 调用。
func (p *Pool) CloseAll() {
	p.mu.Lock()
	all := make([]*Session, 0, len(p.all))
	for s := range p.all {
		all = append(all, s)
	}
	p.all = map[*Session]struct{}{}
	p.idle = map[string][]*Session{}
	p.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}
