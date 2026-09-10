package sftp

import (
	"os"
	"strings"
	"testing"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// 真实抓取的批处理输出：a 有 2 个文件、nope 不存在、empty 存在但为空。
const listManyFixture = `sftp> -ls -la "/tmp/ldm/a"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 one.txt
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 two.txt
sftp> -ls -la "/tmp/ldm/nope"
Can't ls: "/tmp/ldm/nope" not found
sftp> -ls -la "/tmp/ldm/empty"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
`

// 关键断言：不存在的目录必须**缺席**（未知），存在但空的目录必须**在场且为空**。
// 把前者当成空目录会导致整棵子树的假删除。
func TestParseListManyDistinguishesUnknownFromEmpty(t *testing.T) {
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/nope", "/tmp/ldm/empty"}
	got := parseListMany(listManyFixture, paths)

	if _, ok := got["/tmp/ldm/nope"]; ok {
		t.Fatal("不存在的目录必须缺席（未知），不能是空列表")
	}
	empty, ok := got["/tmp/ldm/empty"]
	if !ok {
		t.Fatal("存在但为空的目录必须在场")
	}
	if len(empty) != 0 {
		t.Fatalf("空目录应得到 0 条（. 与 .. 已被 ParseLsLf 过滤），得到 %#v", empty)
	}
	a, ok := got["/tmp/ldm/a"]
	if !ok {
		t.Fatal("a 必须在场")
	}
	if len(a) != 2 || a[0].Name != "one.txt" || a[1].Name != "two.txt" {
		t.Fatalf("a 的条目 = %#v", a)
	}
}

// 回显块少于请求数（批处理被截断）时，多出来的路径必须缺席而不是空。
func TestParseListManyMissingBlocksAreUnknown(t *testing.T) {
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/never-listed"}
	got := parseListMany(listManyFixture, paths)
	if _, ok := got["/tmp/ldm/never-listed"]; ok {
		t.Fatal("没有对应输出块的路径必须缺席")
	}
}

// 错误行是 CRLF（实测），分段与判定都必须容忍 CR。
func TestParseListManyToleratesCRLFErrors(t *testing.T) {
	fixture := `sftp> -ls -la "/x"` + "\r\n" + `Can't ls: "/x" not found` + "\r\n"
	if got := parseListMany(fixture, []string{"/x"}); len(got) != 0 {
		t.Fatalf("CRLF 错误块必须判为未知，得到 %#v", got)
	}
}

// ListMany 必须给每条命令加 '-' 前缀(否则任一路径失败会中止整批，后面目录永远列不出来)，
// 把结果按 path 归属，并按裁决 3 发开始/完成事件。
func TestListManyBuildsDashPrefixedBatchAndLogs(t *testing.T) {
	var gotBatch string
	var events []forward.Event
	run := func(name string, args ...string) (osutil.Outcome, error) {
		for i, a := range args {
			if a == "-b" && i+1 < len(args) {
				b, _ := os.ReadFile(args[i+1])
				gotBatch = string(b)
			}
		}
		return osutil.Outcome{Stdout: listManyFixture}, nil
	}
	c := NewCtrl(run, func(e forward.Event) { events = append(events, e) })
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/nope", "/tmp/ldm/empty"}
	got, err := c.ListMany("ai", "", paths)
	if err != nil {
		t.Fatalf("ListMany: %v", err)
	}
	want := "-ls -la \"/tmp/ldm/a\"\n-ls -la \"/tmp/ldm/nope\"\n-ls -la \"/tmp/ldm/empty\"\n"
	if gotBatch != want {
		t.Fatalf("batch = %q, want %q", gotBatch, want)
	}
	if _, ok := got["/tmp/ldm/nope"]; ok {
		t.Fatal("失败目录必须缺席(未知)")
	}
	if items, ok := got["/tmp/ldm/empty"]; !ok || len(items) != 0 {
		t.Fatalf("空目录必须在场且为空, got %#v ok=%v", items, ok)
	}
	if len(events) < 2 {
		t.Fatalf("expected start+done events, got %+v", events)
	}
	if events[0].SourceType != "sftp" || events[0].SourceID != "ai" || events[0].Message != "sftp ls many (3 dirs)" {
		t.Fatalf("unexpected start event: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Level != "info" || last.Message != "sftp ls many done (2/3 dirs)" {
		t.Fatalf("unexpected done event: %+v", last)
	}
}

// 空请求不应调用 runner，也不应报错。
func TestListManyEmptyPathsIsNoop(t *testing.T) {
	called := false
	c := NewCtrl(func(name string, args ...string) (osutil.Outcome, error) {
		called = true
		return osutil.Outcome{}, nil
	}, nil)
	got, err := c.ListMany("ai", "", nil)
	if err != nil {
		t.Fatalf("ListMany: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %#v", got)
	}
	if called {
		t.Fatal("runner must not be called for empty paths")
	}
}

// 进程失败时必须发 error 事件并返回错误(不能吞掉)。
func TestListManyFailureLogsError(t *testing.T) {
	var events []forward.Event
	c := NewCtrl(func(name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 1, Stderr: "Permission denied (publickey)"}, nil
	}, func(e forward.Event) { events = append(events, e) })
	if _, err := c.ListMany("ai", "", []string{"/x"}); err == nil {
		t.Fatal("expected error")
	}
	if len(events) < 2 {
		t.Fatalf("expected start+error events, got %+v", events)
	}
	last := events[len(events)-1]
	if last.Level != "error" || !strings.Contains(last.Message, "Permission denied") {
		t.Fatalf("expected error event with stderr, got %+v", last)
	}
}
