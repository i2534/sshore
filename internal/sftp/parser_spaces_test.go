package sftp

import "testing"

// —— S4：ParseLsLf 必须保留文件名里的连续空格/首尾空格，且异常行不产出空名条目 ——
//
// 旧实现用 strings.Fields 把空白游程压成一个，`a  b.txt`（双空格）被解析成 `a b.txt`；
// 名字首尾空格被 TrimSpace 吃掉；恰好 5 字段的错误行（如 `Can't ls: "/x" not found`）
// 产出 Name="" 的条目。期望值均为手工推导的字面量。

// TestParseLsLfPreservesConsecutiveSpaces：双空格/三空格文件名还原为字面量原名。
func TestParseLsLfPreservesConsecutiveSpaces(t *testing.T) {
	out := "-rw-r--r--    1 user group       45 Aug 25 2024 a  b.txt\n" +
		"-rw-r--r--    1 user group       46 Aug 25 2024 a   b.txt\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d: %+v", len(items), items)
	}
	if items[0].Name != "a  b.txt" {
		t.Fatalf("双空格名应还原为字面量, got %q", items[0].Name)
	}
	if items[1].Name != "a   b.txt" {
		t.Fatalf("三空格名应还原为字面量, got %q", items[1].Name)
	}
	if items[0].Size != 45 || items[1].Size != 46 {
		t.Fatalf("size 不对: %+v", items)
	}
	// 日期段（裸年份）仍要正确解析，且不含文件名。
	if items[0].ModTime != "2024-08-25" {
		t.Fatalf("modTime 应为 2024-08-25, got %q", items[0].ModTime)
	}
}

// TestParseLsLfPreservesLeadingTrailingSpaces：名字自身的首尾空格要能还原。
// 取舍：ls 输出里名字紧跟日期后的分隔空白，按「日期后第一个空白为分隔、其余保留」处理。
func TestParseLsLfPreservesLeadingTrailingSpaces(t *testing.T) {
	out := "-rw-r--r--    1 user group       45 Aug 25 2024  lead.txt\n" + // 日期后两个空格 ⇒ 名字前导一个空格
		"-rw-r--r--    1 user group       46 Aug 25 2024 trail.txt \n" // 名字尾随一个空格
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d: %+v", len(items), items)
	}
	if items[0].Name != " lead.txt" {
		t.Fatalf("前导空格名应还原, got %q", items[0].Name)
	}
	if items[1].Name != "trail.txt " {
		t.Fatalf("尾随空格名应还原, got %q", items[1].Name)
	}
}

// TestParseLsLfSkipsErrorLineWithoutName：恰好 5 字段的错误行不得产出 Name="" 的条目。
func TestParseLsLfSkipsErrorLineWithoutName(t *testing.T) {
	out := "Can't ls: \"/x\" not found\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("5 字段错误行不得产出条目, got %+v", items)
	}
}

// TestParseLsLfMixedErrorAndValid：错误行与正常行混排时，只产出正常行（且名字空格保留）。
func TestParseLsLfMixedErrorAndValid(t *testing.T) {
	out := "total 8\n" +
		"Can't ls: \"/x\" not found\n" +
		"-rw-r--r--    1 user group       12 Aug 25 2024 my  file.txt\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("只应产出 1 条正常项, got %+v", items)
	}
	if items[0].Name != "my  file.txt" || items[0].Size != 12 {
		t.Fatalf("正常项不对: %+v", items[0])
	}
}

// TestParseLsLfPaddedDay：日期段日字段右对齐补空格（"Aug  5"）时 ModTime 仍正确。
func TestParseLsLfPaddedDay(t *testing.T) {
	out := "-rw-r--r--    1 user group       12 Aug  5 2024 pad.txt\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 1 || items[0].Name != "pad.txt" {
		t.Fatalf("pad.txt 解析不对: %+v", items)
	}
	if items[0].ModTime != "2024-08-05" {
		t.Fatalf("补空格日字段 modTime 应为 2024-08-05, got %q", items[0].ModTime)
	}
}
