package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPickArchiveAndChecksums(t *testing.T) {
	rel := Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: "sshore-v0.7.0-linux-amd64.tar.gz", URL: "https://github.com/a/b.tar.gz", Size: 8_412_345},
		{Name: "sshore-v0.7.0-windows-amd64.zip", URL: "https://github.com/a/b.zip", Size: 9_123_456},
		{Name: "checksums.txt", URL: "https://github.com/a/checksums.txt", Size: 512},
	}}
	lin, err := PickArchive(rel, "linux", "amd64")
	if err != nil || lin.Name != "sshore-v0.7.0-linux-amd64.tar.gz" {
		t.Fatalf("linux 挑选失败: %v %+v", err, lin)
	}
	if lin.Size != 8_412_345 {
		t.Fatalf("linux 资产 size = %d, want 8412345", lin.Size)
	}
	win, err := PickArchive(rel, "windows", "amd64")
	if err != nil || win.Name != "sshore-v0.7.0-windows-amd64.zip" {
		t.Fatalf("windows 挑选失败: %v %+v", err, win)
	}
	if win.Size != 9_123_456 {
		t.Fatalf("windows 资产 size = %d, want 9123456", win.Size)
	}
	if _, err := PickArchive(rel, "windows", "386"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("386 必须 ErrNoAsset, got %v", err)
	}
	cs, err := PickChecksums(rel)
	if err != nil || cs.Name != "checksums.txt" {
		t.Fatalf("校验文件挑选失败: %v %+v", err, cs)
	}
	if cs.Size != 512 {
		t.Fatalf("checksums size = %d, want 512", cs.Size)
	}
	if _, err := PickChecksums(Release{Tag: "v0.7.0"}); !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("缺校验文件必须 ErrNoChecksum, got %v", err)
	}
}

// TestLatestAssetSizeJSON 覆盖 upstream assets[].size 的解析：
// 携带 size 时透传到 Asset.Size；缺失时（旧源/未提供）必须为 0，调用方按 0 处理。
func TestLatestAssetSizeJSON(t *testing.T) {
	const head = `{"tag_name":"v0.7.0","assets":[{"name":"sshore-v0.7.0-linux-amd64.tar.gz","browser_download_url":"https://github.com/a/b.tar.gz"`
	cases := []struct {
		name string
		body string
		want int64
	}{
		{"携带 size", head + `,"size":123456}]}`, 123456},
		{"缺失 size", head + `}]}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := &Client{HTTP: doerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(c.body)),
				}, nil
			})}
			rel, err := cl.Latest(context.Background(), "https://api.github.com/repos/i2534/sshore")
			if err != nil {
				t.Fatal(err)
			}
			if len(rel.Assets) != 1 {
				t.Fatalf("assets = %+v, want 1 条", rel.Assets)
			}
			if got := rel.Assets[0].Size; got != c.want {
				t.Fatalf("Asset.Size = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		src, raw string
		want     bool
	}{
		{"https://api.github.com/repos/i2534/sshore", "https://github.com/i2534/sshore/releases/download/v0.7.0/a.tar.gz", true},
		{"https://api.github.com/repos/i2534/sshore", "https://objects.githubusercontent.com/github-production-release-asset/x", true},
		{"https://api.github.com/repos/i2534/sshore", "https://evil.example.com/a.tar.gz", false},
		{"https://mirror.corp:8443/api/repos/sshore", "https://mirror.corp:8443/files/a.tar.gz", true},
		{"https://mirror.corp:8443/api/repos/sshore", "https://other.corp/a.tar.gz", false},
	}
	for _, c := range cases {
		if got := SameOrigin(c.src, c.raw); got != c.want {
			t.Errorf("SameOrigin(%q, %q) = %v, want %v", c.src, c.raw, got, c.want)
		}
	}
}
