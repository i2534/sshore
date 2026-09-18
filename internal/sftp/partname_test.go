package sftp

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestPartNameMaxLiteralIsPinned(t *testing.T) {
	if partNameMax != 200 {
		t.Fatalf("临时名长度阈值漂移：%d（spec 与计划都写死 200 字节）", partNameMax)
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

// TestRemoteFamilyBoundaryIsPinned 钉住远端 POSIX 家族的两个非退化分支与 200 字节阈值本身。
// 旧 TestRemoteFamilyUsesPosixSeparators 只走退化分支且只断言 len>partNameMax 的弱口径，
// 无法杀死「PartNameRemote/BakNameRemote 恒退化」与「阈值漂移」三类变异（重审 N1）。
func TestRemoteFamilyBoundaryIsPinned(t *testing.T) {
	id8 := "abcdef12"
	// PartNameRemote 与本地家族同口径：
	// len(target) = 200 - 17(marker) - 8(id) - 1('-') - 6(rand) = 168
	target := "/data/" + strings.Repeat("y", partNameMax-len("/data/")-len(PartMarker)-len(id8)-1-6)
	if len(target) != 168 {
		t.Fatalf("测试自身前提错误：target len=%d，应为 168", len(target))
	}
	got := PartNameRemote(target, id8)
	if len(got) != partNameMax {
		t.Fatalf("远端恰好 200 字节不得退化：want len=%d, got %d (%q)", partNameMax, len(got), got)
	}
	if !strings.HasPrefix(got, target+PartMarker) {
		t.Fatalf("远端未退化时必须保留 <target><marker> 形态，got %q", got)
	}

	// BakNameRemote 的后缀是 "bak-"(4) 而非 id(8)+"-"(1)，故边界 target 长度为 200-17-4-6=173；
	// 用 PartName 的 target 会得到 195 字节，钉不住 bak 的阈值。
	bakTarget := "/data/" + strings.Repeat("z", partNameMax-len("/data/")-len(PartMarker)-len("bak-")-6)
	if len(bakTarget) != 173 {
		t.Fatalf("测试自身前提错误：bakTarget len=%d，应为 173", len(bakTarget))
	}
	bak := BakNameRemote(bakTarget)
	if len(bak) != partNameMax {
		t.Fatalf("远端 bak 恰好 200 字节不得退化：want len=%d, got %d (%q)", partNameMax, len(bak), bak)
	}
	if !strings.HasPrefix(bak, bakTarget+PartMarker) {
		t.Fatalf("远端 bak 未退化时必须保留 <target><marker> 形态，got %q", bak)
	}

	// 超一字节：两个函数都必须退化，且留在同一 POSIX 目录、被 IsInternalTemp 认出
	got2 := PartNameRemote(target+"y", id8)
	if len(got2) >= partNameMax {
		t.Fatalf("远端超限必须退化为短名（<阈值），got len=%d (%q)", len(got2), got2)
	}
	if path.Dir(got2) != "/data" {
		t.Fatalf("远端超限退化必须留在同一 POSIX 目录，got %q", got2)
	}
	if !IsInternalTemp(got2) {
		t.Fatalf("远端退化短名必须被 IsInternalTemp 认出，got %q", got2)
	}
	bak2 := BakNameRemote(bakTarget + "z")
	if len(bak2) >= partNameMax {
		t.Fatalf("远端 bak 超限必须退化为短名（<阈值），got len=%d (%q)", len(bak2), bak2)
	}
	if path.Dir(bak2) != "/data" {
		t.Fatalf("远端 bak 超限退化必须留在同一 POSIX 目录，got %q", bak2)
	}
	if !IsInternalTemp(bak2) {
		t.Fatalf("远端 bak 退化短名必须被 IsInternalTemp 认出，got %q", bak2)
	}
}

// TestShortIDBoundaryAndNonASCII 钉住 shortID 的 rune 语义与 8 字符边界本身（Minor-4）：
// 把 []rune 改成 []byte、或把 8 改成 9，本测试都必须失败。
func TestShortIDBoundaryAndNonASCII(t *testing.T) {
	// 恰好 9 个字符：必须只剩前 8 个（钉住 8 这个边界，不只是「截断」）
	if got := PartName("/d/a.txt", "123456789"); !strings.Contains(got, "12345678-") {
		t.Fatalf("恰好 9 字符 id 必须截为前 8，got %q", got)
	}
	// 恰好 8 个字符：原样保留
	if got := PartName("/d/a.txt", "12345678"); !strings.Contains(got, "12345678-") {
		t.Fatalf("恰好 8 字符 id 必须原样，got %q", got)
	}
	// 非 ASCII：按 rune 截断，不得切断 UTF-8（9 个汉字截为前 8 个，结果必须是合法 UTF-8）
	got := PartName("/d/a.txt", "一二三四五六七八九")
	if !strings.Contains(got, "一二三四五六七八-") {
		t.Fatalf("多字节 id 必须按 rune 截断，got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断后必须仍是合法 UTF-8，got %q", got)
	}
}
