package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	const h1 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	const h2 = "486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7"
	in := h1 + "  sshore-v0.7.0-linux-amd64.tar.gz\r\n" +
		h2 + " *sshore-v0.7.0-windows-amd64.zip\r\n" +
		"\r\n"
	m, err := ParseChecksums(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m["sshore-v0.7.0-linux-amd64.tar.gz"] != h1 {
		t.Fatalf("tar.gz 哈希解析错误: %v", m)
	}
	if m["sshore-v0.7.0-windows-amd64.zip"] != h2 {
		t.Fatalf("二进制前缀 * 未处理: %v", m)
	}
}

// F3：哈希长度非法与空输入都必须整份拒绝。
func TestParseChecksumsRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"哈希长度非法", "deadbeef  x\n"},
		{"空输入", ""},
	}
	for _, c := range cases {
		if m, err := ParseChecksums(strings.NewReader(c.in)); err == nil {
			t.Errorf("%s: 必须报错（得到 %v）", c.name, m)
		}
	}
}

func TestVerifyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// echo -n hello | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := VerifyFile(p, want); err != nil {
		t.Fatalf("期望通过: %v", err)
	}
	if err := VerifyFile(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("哈希不符必须报错")
	}
}
