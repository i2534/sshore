package sftp

import (
	"path"
	"path/filepath"
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

func TestPartNameBoundaryIsPinned(t *testing.T) {
	id8 := "abcdef12"
	// 恰好压线：len(target) = 200 - 17(marker) - 8(id) - 1('-') - 6(rand) = 168
	target := "/data/" + strings.Repeat("y", partNameMax-len("/data/")-len(PartMarker)-len(id8)-1-6)
	if len(target) != 168 {
		t.Fatalf("测试自身前提错误：target len=%d，应为 168", len(target))
	}
	got := PartName(target, id8)
	if len(got) != partNameMax {
		t.Fatalf("恰好 200 字节不得退化：want len=%d, got %d (%q)", partNameMax, len(got), got)
	}
	if !strings.HasPrefix(got, target+PartMarker) {
		t.Fatalf("未退化时必须保留 <target><marker> 形态，got %q", got)
	}

	// 超一字节：必须退化，且仍留在同目录、可被 IsInternalTemp 认出
	longer := target + "y"
	got2 := PartName(longer, id8)
	if len(got2) > partNameMax {
		t.Fatalf("超限必须退化，got len=%d (%q)", len(got2), got2)
	}
	if filepath.Dir(got2) != filepath.Dir(longer) {
		t.Fatalf("退化短名必须留在同目录（否则 rename 不原子），got %q", got2)
	}
	if !strings.HasPrefix(filepath.Base(got2), PartMarker) {
		t.Fatalf("退化短名必须以 marker 开头，got %q", got2)
	}
	if !IsInternalTemp(got2) {
		t.Fatalf("退化短名必须被 IsInternalTemp 认出，got %q", got2)
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

func TestMarkerLiteralIsPinned(t *testing.T) {
	if PartMarker != ".sshore-sftppart-" {
		t.Fatalf("marker 字面量漂移：%q（前端 Task 14 硬编码同一字面量，spec:271）", PartMarker)
	}
}

func TestShortIDTruncationAndAnon(t *testing.T) {
	if got := PartName("/d/a.txt", "abcdef123456"); !strings.Contains(got, "abcdef12-") {
		t.Fatalf("id 超过 8 必须截前 8 并紧跟连字符，got %q", got)
	}
	if got := PartName("/d/a.txt", ""); !strings.Contains(got, "anon-") {
		t.Fatalf("空 id 必须回落 anon，got %q", got)
	}
}

func TestBakNameDegradesToSameDir(t *testing.T) {
	long := "/data/" + strings.Repeat("y", 200) + ".bin"
	bak := BakName(long)
	if len(bak) > partNameMax {
		t.Fatalf("bak 超长必须退化，got len=%d (%q)", len(bak), bak)
	}
	if filepath.Dir(bak) != filepath.Dir(long) {
		t.Fatalf("bak 退化必须留在同目录，got %q", bak)
	}
	if !IsInternalTemp(bak) {
		t.Fatalf("bak 必须被 IsInternalTemp 认出，got %q", bak)
	}
}

func TestIsInternalTempUsesBaseOnly(t *testing.T) {
	// marker 出现在父目录名里时，普通文件不得被判为内部临时文件
	if IsInternalTemp("/data/.sshore-sftppart-x/notes.txt") {
		t.Fatal("判定必须只看 basename：父目录含 marker 不应命中")
	}
}

func TestRemoteFamilyUsesPosixSeparators(t *testing.T) {
	long := "/data/" + strings.Repeat("y", 200) + ".bin"
	got := PartNameRemote(long, "abcdef12")
	if len(got) > partNameMax {
		t.Fatalf("远端退化名超长：%q", got)
	}
	if path.Dir(got) != path.Dir(long) {
		t.Fatalf("远端退化名必须留在同一 POSIX 目录：got %q", got)
	}
	if strings.Contains(got, "\\") {
		t.Fatalf("远端路径不得出现反斜杠：%q", got)
	}
	bak := BakNameRemote(long)
	if strings.Contains(bak, "\\") || path.Dir(bak) != path.Dir(long) {
		t.Fatalf("BakNameRemote 必须 POSIX 同目录：%q", bak)
	}
}
