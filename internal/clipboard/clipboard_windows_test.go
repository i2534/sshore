//go:build windows

package clipboard

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件的用例由 CI 的 go-windows job（windows-latest，go vet + go test -count=1）真跑；
// 本机（Linux）只能验证它不破坏 GOOS=windows 的编译与 vet。

func TestSupportTrueOnWindows(t *testing.T) {
	if !Support() {
		t.Error("Windows 上 Support() 必须是 true")
	}
}

func TestSetFilesMissingPathErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-there.txt")
	err := SetFiles([]string{missing})
	if err == nil {
		t.Fatal("路径不存在时必须返回错误，而不是去清空剪贴板")
	}
	if !strings.Contains(err.Error(), "路径不存在") {
		t.Errorf("err=%v, want 含「路径不存在」", err)
	}
}

func TestBuildHDropBytesWindowsBoundary(t *testing.T) {
	// 路径均为 ASCII ⇒ UTF-16 码元数 == 字节数。
	cases := []struct {
		name  string
		paths []string
		want  int
	}{
		{"nil", nil, 20 + 2},
		{"single", []string{`C:\a.txt`}, 20 + 2*(len(`C:\a.txt`)+1) + 2},
		{"multi", []string{`C:\a.txt`, `D:\b.txt`}, 20 + 2*(len(`C:\a.txt`)+1) + 2*(len(`D:\b.txt`)+1) + 2},
	}
	for _, c := range cases {
		b := buildHDropBytes(c.paths)
		if len(b) != c.want {
			t.Errorf("%s: len=%d, want %d", c.name, len(b), c.want)
		}
	}
}
