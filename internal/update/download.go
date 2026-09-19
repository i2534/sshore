package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// DownloadOpt 控制下载行为；IdleTimeout 是「连续无字节」上限，Throttle 是进度最小间隔。
type DownloadOpt struct {
	IdleTimeout time.Duration
	Throttle    time.Duration
	Progress    func(done, total int64)
}

// Download 把 url 流式写到 dest（失败/取消会删除 dest）。
func (c *Client) Download(ctx context.Context, url, dest string, opt DownloadOpt) error {
	if c.HTTP == nil {
		return errors.New("未配置 HTTP 客户端")
	}
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 30 * time.Second
	}
	if opt.Throttle <= 0 {
		opt.Throttle = 200 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fail := func(e error) error {
		_ = out.Close()
		_ = os.Remove(dest)
		return e
	}
	// 空闲看门狗：每次成功读到字节就重置；触发即标记原因并取消 ctx。
	var idleHit atomic.Bool
	watchdog := time.AfterFunc(opt.IdleTimeout, func() { idleHit.Store(true); cancel() })
	defer watchdog.Stop()
	total := resp.ContentLength
	var done int64
	last := time.Now()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			done += int64(n)
			watchdog.Reset(opt.IdleTimeout)
			if opt.Progress != nil && time.Since(last) >= opt.Throttle {
				last = time.Now()
				opt.Progress(done, total)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			if errors.Is(rerr, context.Canceled) && ctx.Err() != nil {
				if idleHit.Load() {
					return fail(fmt.Errorf("下载空闲超时（%s 无数据）", opt.IdleTimeout))
				}
				return fail(ctx.Err())
			}
			return fail(rerr)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if opt.Progress != nil {
		opt.Progress(done, total)
	}
	return nil
}

// FreeSpace 的实现按平台拆到 freespace_unix.go / freespace_windows.go（见下），
// 这样 download.go 不引入平台专有符号，GOOS=windows 与 linux 都能编译。
