package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz 造一个含指定条目的 tar.gz。
func makeTarGz(t *testing.T, entries map[string]struct {
	body []byte
	link byte
	mode int64
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, e := range entries {
		hdr := &tar.Header{Name: name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: e.link}
		switch e.link {
		case tar.TypeSymlink:
			hdr.Linkname = "other" // 无关 symlink：策略要求跳过而非拒绝
		case tar.TypeLink:
			hdr.Linkname = "sshore" // hardlink 指向目标二进制：策略要求整档拒绝
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractBinaryTarGzAcceptsDotSlashPrefix(t *testing.T) {
	// 真实 Release 的 tar.gz 里是 ./sshore（带 ./ 前缀，mode 0755）—— F6 实测。
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{
		"./sshore":    {[]byte("NEW"), tar.TypeReg, 0o755},
		"./README.md": {[]byte("doc"), tar.TypeReg, 0o644},
	})
	arc := writeTemp(t, data, "a.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "linux", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "NEW" {
		t.Fatalf("提取内容错误: %q %v", got, err)
	}
}

func TestExtractBinaryZipRootName(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("sshore.exe")
	_, _ = w.Write([]byte("NEWEXE"))
	_, _ = zw.Create("README.md")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	arc := writeTemp(t, buf.Bytes(), "a.zip")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "windows", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "NEWEXE" {
		t.Fatalf("zip 提取错误: %q", got)
	}
}

func TestExtractBinaryRejectsSymlinkTraversalAndMultiMatch(t *testing.T) {
	linkOnly := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{"sshore": {nil, tar.TypeSymlink, 0o777}})
	cases := []struct {
		name string
		data []byte
		goos string
	}{
		{"符号链接", linkOnly, "linux"},
		{"目标硬链接", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"sshore": {nil, tar.TypeLink, 0o755}}), "linux"},
		{"硬链接 Linkname 指向目标", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"alias": {nil, tar.TypeLink, 0o755}}), "linux"},
		{"路径遍历", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"../sshore": {[]byte("X"), tar.TypeReg, 0o755}}), "linux"},
		{"多份匹配", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"sshore": {[]byte("A"), tar.TypeReg, 0o755}, "./sshore": {[]byte("B"), tar.TypeReg, 0o755}}), "linux"},
		{"没有匹配", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"README.md": {[]byte("doc"), tar.TypeReg, 0o644}}), "linux"},
	}
	for _, c := range cases {
		arc := writeTemp(t, c.data, "x.tar.gz")
		dest := filepath.Join(t.TempDir(), "out")
		err := ExtractBinary(arc, c.goos, dest)
		if err == nil {
			t.Errorf("%s: 必须报错", c.name)
			continue
		}
		// F1 回归保护：任何错误路径都不得把 dest 留在磁盘上（含「多份匹配」）。
		if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
			t.Errorf("%s: 错误返回后 dest 必须不存在（stat: %v）", c.name, statErr)
		}
		// F1 回归保护：临时文件也必须被 defer 清干净。
		if ents, readErr := os.ReadDir(filepath.Dir(dest)); readErr == nil && len(ents) != 0 {
			t.Errorf("%s: 错误返回后不得留下残余文件 %v", c.name, ents)
		}
	}
}

func TestExtractBinaryRejectsOversizeEntry(t *testing.T) {
	old := maxBinarySize
	maxBinarySize = 4
	defer func() { maxBinarySize = old }()
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{"sshore": {[]byte("1234567890"), tar.TypeReg, 0o755}})
	arc := writeTemp(t, data, "big.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "linux", dest); err == nil {
		t.Fatal("超过大小上限必须报错")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("错误返回后 dest 必须不存在（stat: %v）", err)
	}
}

// F2 正向用例：无关 symlink 必须跳过，不得导致整档拒绝。
func TestExtractBinarySkipsUnrelatedSymlink(t *testing.T) {
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{
		"README.md": {nil, tar.TypeSymlink, 0o777},
		"sshore":    {[]byte("NEW"), tar.TypeReg, 0o755},
	})
	arc := writeTemp(t, data, "ok.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "linux", dest); err != nil {
		t.Fatalf("无关 symlink 不得导致拒绝: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "NEW" {
		t.Fatalf("提取内容错误: %q %v", got, err)
	}
}

// F2 正向用例（zip 分支）：无关 symlink 同样必须跳过。
func TestExtractBinaryZipSkipsUnrelatedSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	linkHdr := &zip.FileHeader{Name: "README.md", Method: zip.Store}
	linkHdr.SetMode(os.ModeSymlink | 0o777)
	lw, err := zw.CreateHeader(linkHdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Write([]byte("sshore.exe")); err != nil {
		t.Fatal(err)
	}
	w, _ := zw.Create("sshore.exe")
	_, _ = w.Write([]byte("NEWEXE"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	arc := writeTemp(t, buf.Bytes(), "ok.zip")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "windows", dest); err != nil {
		t.Fatalf("zip 无关 symlink 不得导致拒绝: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "NEWEXE" {
		t.Fatalf("zip 提取错误: %q", got)
	}
}
