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
		if e.link == tar.TypeSymlink {
			hdr.Linkname = "other"
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
		if err := ExtractBinary(arc, c.goos, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Errorf("%s: 必须报错", c.name)
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
	if err := ExtractBinary(arc, "linux", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("超过大小上限必须报错")
	}
}
