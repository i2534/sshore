package clipboard

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// utf16leBytes 是测试自备的编码助手：把字符串编码成 UTF-16LE 字节序列。
func utf16leBytes(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[i*2:], u)
	}
	return b
}

func TestBuildHDropBytesHeader(t *testing.T) {
	b := buildHDropBytes([]string{"C:\\a.txt"})
	if len(b) < 20 {
		t.Fatalf("len=%d, want >= 20", len(b))
	}
	if got := binary.LittleEndian.Uint32(b[0:4]); got != 20 {
		t.Errorf("pFiles=%d, want 20", got)
	}
	for _, off := range []int{4, 8, 12} {
		if got := binary.LittleEndian.Uint32(b[off : off+4]); got != 0 {
			t.Errorf("field@%d=%d, want 0", off, got)
		}
	}
	if got := binary.LittleEndian.Uint32(b[16:20]); got != 1 {
		t.Errorf("fWide=%d, want 1", got)
	}
}

func TestBuildHDropBytesPayload(t *testing.T) {
	const p = "C:\\a.txt"
	b := buildHDropBytes([]string{p})
	units := len(utf16.Encode([]rune(p)))
	want := 20 + 2*(units+1) + 2
	if len(b) != want {
		t.Fatalf("len=%d, want %d", len(b), want)
	}
	if got := binary.LittleEndian.Uint32(b[len(b)-4:]); got != 0 {
		t.Errorf("tail=% x, want 00 00 00 00", b[len(b)-4:])
	}
}

func TestBuildHDropBytesMultiPath(t *testing.T) {
	first := "C:\\a.txt"
	second := "D:\\b.txt"
	b := buildHDropBytes([]string{first, second})

	if got := binary.LittleEndian.Uint32(b[len(b)-4:]); got != 0 {
		t.Errorf("tail=% x, want 00 00 00 00", b[len(b)-4:])
	}

	// 第二条路径的载荷必须逐字等于 second 的 UTF-16LE + 路径自身结尾 \0 码元 + 列表结束 \0 码元（共 4 字节）。
	off := 20 + 2*(len(utf16.Encode([]rune(first)))+1)
	want := append(utf16leBytes(second), 0, 0, 0, 0)
	if got := b[off:]; string(got) != string(want) {
		t.Errorf("second path payload=% x, want % x", got, want)
	}
}

func TestBuildHDropBytesNonASCII(t *testing.T) {
	const p = "C:\\中文 目录\\it's 😀.txt"
	b := buildHDropBytes([]string{p})

	units := make([]uint16, 0, (len(b)-20)/2)
	for i := 20; i+2 <= len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	// 必须先剥掉尾部连续的 \0 码元（路径自身一个 + 列表结束一个），否则解码结果带两个 \x00。
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	if got := string(utf16.Decode(units)); got != p {
		t.Errorf("got %q, want %q", got, p)
	}
}

func TestBuildHDropBytesEmpty(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		b := buildHDropBytes(in)
		if len(b) != 22 {
			t.Fatalf("len=%d, want 22 (20 字节头 + 一个 \\0 码元)", len(b))
		}
		if got := binary.LittleEndian.Uint16(b[20:22]); got != 0 {
			t.Errorf("terminator=%d, want 0", got)
		}
	}
}
