package update

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// F7 回归：归档把二进制放在名为 sshore 的目录里（条目 "sshore/"，TypeDir）
// 时，cleanEntry 归一后 basename==sshore，旧 isTargetLink 不区分条目类型，
// 把目录误判成「目标链接」整档拒绝。zip 分支对目录是 continue，tar 必须对齐：
// isTargetLink 只对 symlink/hardlink 生效，其余非普通条目一律跳过。
func TestExtractBinaryTarGzAcceptsTargetNamedDir(t *testing.T) {
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{
		"sshore/":       {nil, tar.TypeDir, 0o755},
		"sshore/sshore": {[]byte("NEW"), tar.TypeReg, 0o755},
	})
	arc := writeTemp(t, data, "dir.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "linux", dest); err != nil {
		t.Fatalf("名为 sshore 的目录条目不得导致整档拒绝: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "NEW" {
		t.Fatalf("提取内容错误: %q %v", got, err)
	}
}

// F7 守卫（windows 同形）：sshore/ 目录 + sshore/sshore.exe 普通文件同样可解。
func TestExtractBinaryTarGzAcceptsTargetNamedDirWindows(t *testing.T) {
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{
		"sshore/":           {nil, tar.TypeDir, 0o755},
		"sshore/sshore.exe": {[]byte("NEWEXE"), tar.TypeReg, 0o755},
	})
	arc := writeTemp(t, data, "dir-win.tar.gz")
	dest := filepath.Join(t.TempDir(), "out.exe")
	if err := ExtractBinary(arc, "windows", dest); err != nil {
		t.Fatalf("windows：名为 sshore 的目录条目不得导致整档拒绝: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "NEWEXE" {
		t.Fatalf("提取内容错误: %q %v", got, err)
	}
}

// F7 守卫：指向目标的 symlink/hardlink 仍被整档拒绝（既有语义不变）。
// 与目录条目混排时也不得放行——链接判定只收窄类型，不放松目标名策略。
func TestExtractBinaryTarGzStillRejectsTargetLinksAlongsideDir(t *testing.T) {
	cases := []struct {
		name string
		link byte
	}{
		{"目标 symlink", tar.TypeSymlink},
		{"目标 hardlink", tar.TypeLink},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := makeTarGz(t, map[string]struct {
				body []byte
				link byte
				mode int64
			}{
				"sshore/": {nil, tar.TypeDir, 0o755},
				"sshore":  {nil, c.link, 0o777},
			})
			arc := writeTemp(t, data, "link.tar.gz")
			dest := filepath.Join(t.TempDir(), "out")
			if err := ExtractBinary(arc, "linux", dest); err == nil {
				t.Fatal("指向目标的链接条目仍必须整档拒绝")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("错误返回后 dest 必须不存在: %v", err)
			}
		})
	}
}
