package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient() *Client {
	return &Client{HTTP: &http.Client{}, UserAgent: "sshore/test"}
}

const latestOKBody = `{"tag_name":"v0.7.0","body":"note","published_at":"2026-09-19T00:00:00Z","assets":[{"name":"sshore-v0.7.0-linux-amd64.tar.gz","browser_download_url":"https://github.com/x/y/releases/download/v0.7.0/a.tar.gz"}]}`

func TestLatestStatusMapping(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		header  map[string]string
		body    string
		wantErr error
	}{
		{"200", http.StatusOK, nil, latestOKBody, nil},
		{"403 限流", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, "", ErrRateLimited},
		{"403 非限流", http.StatusForbidden, nil, "", ErrCheckFailed},
		{"429", http.StatusTooManyRequests, nil, "", ErrRateLimited},
		{"500", http.StatusInternalServerError, nil, "", ErrCheckFailed},
		{"坏 JSON", http.StatusOK, nil, "{oops", ErrCheckFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/releases/latest" {
					t.Errorf("路径错误: %s", r.URL.Path)
				}
				if got := r.Header.Get("User-Agent"); got != "sshore/test" {
					t.Errorf("UA 未设置: %q", got)
				}
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			rel, err := newTestClient().Latest(context.Background(), srv.URL)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.wantErr == nil && (rel.Tag != "v0.7.0" || len(rel.Assets) != 1) {
				t.Fatalf("解析结果错误: %+v", rel)
			}
		})
	}
}

func TestLatestEmptyAssets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.7.0","assets":[]}`))
	}))
	defer srv.Close()
	rel, err := newTestClient().Latest(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PickArchive(rel, "linux", "amd64"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("空 assets 必须 ErrNoAsset, got %v", err)
	}
}
