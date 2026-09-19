package update

import "testing"

func TestClass(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"v0.7.0", KindClean},
		{"0.7.0", KindClean},
		{"v0.6.0-80-gc2d2a36", KindDescribe},
		{"dev", KindDev},
		{"", KindDev},
		{"abc", KindInvalid},
		{"1.2", KindInvalid},
	}
	for _, c := range cases {
		if got := Class(c.in); got != c.want {
			t.Errorf("Class(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCompareIsNumericNotLexical(t *testing.T) {
	if Compare("v0.10.0", "v0.9.0") <= 0 {
		t.Fatal("0.10.0 必须大于 0.9.0（十进制而非字典序）")
	}
	if Compare("v0.7.0", "v0.7.0") != 0 {
		t.Fatal("相同版本必须相等")
	}
}

func TestParsePendingName(t *testing.T) {
	cases := []struct {
		in  string
		ver string
		ok  bool
	}{
		{"sshore.v0.7.0", "v0.7.0", true},
		{"sshore.v0.7.0.exe", "v0.7.0", true},
		{"sshore.dev-20260919-101112", "dev-20260919-101112", true},
		{"sshore.exe", "", false}, // 正式二进制不能是候选（否则版本段会被解析成 "exe"）
		{"sshore", "", false},
		{"sshore-update.sh", "", false},
		{"sshore.v0.7.0.sha256", "", false},
	}
	for _, c := range cases {
		ver, ok := ParsePendingName(c.in)
		if ok != c.ok || ver != c.ver {
			t.Errorf("ParsePendingName(%q) = (%q, %v), want (%q, %v)", c.in, ver, ok, c.ver, c.ok)
		}
	}
}
