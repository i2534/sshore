package sftp

import (
	"context"
	"testing"

	"sshore/internal/osutil"
)

// 复用 P1 Task 8 已放进 internal/sftp/ctrl_test.go 的 mockLs / lsLine（同属 package sftp）。
// **不要在本文件重复定义**：同名函数会让 go test ./internal/sftp/ 直接报 redeclared。

func TestSearchDepthZeroOnlyListsRoot(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("app.log", false, 10), lsLine("sub", true, 0))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 0, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Scanned != 1 || len(out.Hits) != 1 || out.Hits[0].Path != "app.log" {
		t.Fatalf("maxDepth=0 只应列根层: %+v", out)
	}
}

func TestSearchDepthOneRecursesOnce(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("app.log", false, 10), lsLine("sub", true, 0))})
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/sub", lsLine("deep.log", false, 20))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 1, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Scanned != 2 || len(out.Hits) != 2 {
		t.Fatalf("maxDepth=1 应列 / 与 /sub: %+v", out)
	}
	if out.Hits[1].Path != "sub/deep.log" {
		t.Fatalf("相对路径错误: %+v", out.Hits)
	}
}

func TestSearchTruncatesAtLimit(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("a.log", false, 1), lsLine("b.log", false, 2))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 0, 1, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !out.Truncated || len(out.Hits) != 1 {
		t.Fatalf("应截断为 1 条: %+v", out)
	}
}

func TestSearchMarksUnreadableWhenBlockMissing(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	// stdout 为空 ⇒ 没有任何回显块 ⇒ 根路径'未知'，必须计 Unreadable，绝不当作空目录。
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: ""})
	out, err := c.Search(context.Background(), "h", "", "/gone", "", 0, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Unreadable != 1 || out.Scanned != 0 {
		t.Fatalf("缺块必须计为不可读: %+v", out)
	}
}

func TestSearchCancelledReturnsPartial(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := c.Search(ctx, "h", "", "/", "", 5, 100, nil)
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !out.Cancelled {
		t.Fatalf("Cancelled must be true: %+v", out)
	}
}
