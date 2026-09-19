package update

import (
	"errors"
	"testing"
)

func TestPickArchiveAndChecksums(t *testing.T) {
	rel := Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: "sshore-v0.7.0-linux-amd64.tar.gz", URL: "https://github.com/a/b.tar.gz"},
		{Name: "sshore-v0.7.0-windows-amd64.zip", URL: "https://github.com/a/b.zip"},
		{Name: "checksums.txt", URL: "https://github.com/a/checksums.txt"},
	}}
	lin, err := PickArchive(rel, "linux", "amd64")
	if err != nil || lin.Name != "sshore-v0.7.0-linux-amd64.tar.gz" {
		t.Fatalf("linux 挑选失败: %v %+v", err, lin)
	}
	win, err := PickArchive(rel, "windows", "amd64")
	if err != nil || win.Name != "sshore-v0.7.0-windows-amd64.zip" {
		t.Fatalf("windows 挑选失败: %v %+v", err, win)
	}
	if _, err := PickArchive(rel, "windows", "386"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("386 必须 ErrNoAsset, got %v", err)
	}
	cs, err := PickChecksums(rel)
	if err != nil || cs.Name != "checksums.txt" {
		t.Fatalf("校验文件挑选失败: %v %+v", err, cs)
	}
	if _, err := PickChecksums(Release{Tag: "v0.7.0"}); !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("缺校验文件必须 ErrNoChecksum, got %v", err)
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
