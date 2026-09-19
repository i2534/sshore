package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDownloadProgressIsMonotonicAndThrottled(t *testing.T) {
	const total = 3 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(total))
		chunk := make([]byte, 256*1024)
		for i := 0; i < 12; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seen []int64
	client := &Client{HTTP: srv.Client()}
	dest := filepath.Join(t.TempDir(), "a.part")
	err := client.Download(context.Background(), srv.URL, dest, DownloadOpt{
		IdleTimeout: 5 * time.Second,
		Throttle:    time.Millisecond,
		Progress: func(done, got int64) {
			mu.Lock()
			defer mu.Unlock()
			if got != total {
				t.Errorf("total = %d, want %d", got, total)
			}
			seen = append(seen, done)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[len(seen)-1] != total {
		t.Fatalf("最后一帧必须是完成量: %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("进度必须单调: %v", seen)
		}
	}
}

func TestDownloadCancelAndIdleTimeoutRemovePartial(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("start"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()

	// ① 空闲超时：IdleTimeout 200ms 远小于服务端 3s 静默
	dest := filepath.Join(t.TempDir(), "idle.part")
	err := (&Client{HTTP: slow.Client()}).Download(context.Background(), slow.URL, dest,
		DownloadOpt{IdleTimeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("空闲超时必须报错")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("失败后必须删除半截文件")
	}

	// ② ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	dest2 := filepath.Join(t.TempDir(), "cancel.part")
	err = (&Client{HTTP: slow.Client()}).Download(ctx, slow.URL, dest2, DownloadOpt{IdleTimeout: 5 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(dest2); !os.IsNotExist(statErr) {
		t.Fatal("取消后必须删除半截文件")
	}
}

func TestFreeSpace(t *testing.T) {
	n, err := FreeSpace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("可用空间应大于 0: %d", n)
	}
}
