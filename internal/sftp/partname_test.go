package sftp

import (
	"strings"
	"testing"
)

func TestPartNameKeepsTargetPrefixAndMarker(t *testing.T) {
	got := PartName("/data/a.txt", "t1-3")
	if !strings.HasPrefix(got, "/data/a.txt"+PartMarker) {
		t.Fatalf("临时名应保留原名并接中缀，got %q", got)
	}
	if !IsInternalTemp(got) {
		t.Fatalf("IsInternalTemp 必须认出常规名，got %q", got)
	}
	if PartName("/data/a.txt", "t1-3") == PartName("/data/a.txt", "t1-3") {
		t.Fatal("同名两次调用必须不同（随机后缀），否则并发/重试会互相踩")
	}
}

func TestPartNameFallsBackToShortFormWhenTooLong(t *testing.T) {
	long := "/data/" + strings.Repeat("x", 240) + ".bin"
	got := PartName(long, "abcdef123456")
	if len(got) > partNameMax {
		t.Fatalf("超长目标必须退化，got len=%d", len(got))
	}
	// 边界必须钉住（技术审核 M6）：恰好 200 字节不退化、201 字节必须退化
	id8 := "abcdef12"
	exact := "/data/" + strings.Repeat("y", partNameMax-len("/data/")-len(PartMarker)-len(id8)-1-6)
	short := strings.Replace(exact, "yyyyy", "yyyyyyy", 1)
	if n := len(PartName(exact, id8)); n > partNameMax {
		t.Fatalf("恰好在阈值内不应退化，got len=%d", n)
	}
	if n := len(PartName(short, id8)); n > partNameMax {
		t.Fatalf("超过阈值必须退化，got len=%d", n)
	}
	if !IsInternalTemp(got) {
		t.Fatalf("退化短名也必须被 IsInternalTemp 认出（中缀判定），got %q", got)
	}
	if !strings.HasPrefix(got, "/data/") {
		t.Fatalf("退化短名必须留在同目录（rename 才能原子），got %q", got)
	}
}

func TestBakNameIsInternalTempToo(t *testing.T) {
	got := BakName("/data/a.txt")
	if !IsInternalTemp(got) {
		t.Fatalf("bak 必须复用同一中缀，否则成孤儿（spec M2），got %q", got)
	}
}

func TestIsInternalTempRejectsNormalFiles(t *testing.T) {
	for _, n := range []string{"a.txt", "a.txt.bak", ".sshore-part-abc-1", "sshore-sftppart"} {
		if IsInternalTemp(n) {
			t.Fatalf("普通文件 %q 不应被判为内部临时文件", n)
		}
	}
}
