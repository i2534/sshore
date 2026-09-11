package sftp

import (
	"os"
	"strings"
	"testing"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// 真实抓取的批处理 stdout：a 有 2 个文件、nope 不存在、empty 存在但为空。
// 关键：OpenSSH sftp 把客户端错误行写到 **stderr**，因此失败目录在 stdout 里
// 只有一行回显、没有任何列表内容（错误原文见 listManyStderr）。
const listManyFixture = `sftp> -ls -la "/tmp/ldm/a"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 one.txt
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 two.txt
sftp> -ls -la "/tmp/ldm/nope"
sftp> -ls -la "/tmp/ldm/empty"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
`

// 同批次的 stderr（实测）：只有失败目录的客户端错误行，行尾是 CRLF。
const listManyStderr = "Can't ls: \"/tmp/ldm/nope\" not found\r\n"

// 关键断言：不存在的目录必须**缺席**（未知），存在但空的目录必须**在场且为空**。
// 把前者当成空目录会导致整棵子树的假删除。
// 这是回归测试：真机 stdout 里失败目录只有回显行、没有错误行，旧实现把它当成了空目录。
func TestParseListManyDistinguishesUnknownFromEmpty(t *testing.T) {
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/nope", "/tmp/ldm/empty"}
	got := parseListMany(listManyFixture, paths)

	if items, ok := got["/tmp/ldm/nope"]; ok {
		t.Fatalf("stdout 只有回显行的失败目录必须缺席(未知), 得到 %#v", items)
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

// CRLF 容忍：回显行与列表行都带 CR 时仍要正确分段；
// 回显后无内容的块判未知，含 . / .. 的块判在场且为空。
func TestParseListManyToleratesCRLFStdout(t *testing.T) {
	fixture := "sftp> -ls -la \"/x\"\r\n" +
		"sftp> -ls -la \"/y\"\r\n" +
		"drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .\r\n" +
		"drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..\r\n"
	got := parseListMany(fixture, []string{"/x", "/y"})
	if _, ok := got["/x"]; ok {
		t.Fatal("CRLF 下回显后无列表内容的块必须判未知")
	}
	y, ok := got["/y"]
	if !ok || len(y) != 0 {
		t.Fatalf("/y 应为在场且为空, got %#v ok=%v", y, ok)
	}
}

// blockFailed 是次级防御（主不变量是「空块=未知」）：
// 只认紧凑前缀，正常列表行不得因文件名含 "not found" 而误判。
func TestBlockFailedTightPrefixes(t *testing.T) {
	if !blockFailed("Can't ls: \"/x\" not found\r\n") {
		t.Fatal("Can't 前缀(含 CRLF)应判失败")
	}
	if blockFailed("-rw-r--r--    1 lan      lan 4 Sep 10 20:46 not found.txt\n") {
		t.Fatal("正常列表行不得因文件名含 not found 而误判失败")
	}
	if blockFailed(".\n..\n") {
		t.Fatal("空目录列表不得判失败")
	}
}

// stderr 是客户端错误行的真实来源(实测),行尾是 CR;
// 按请求路径反查应能取回原文,未失败的路径不得命中。
func TestStderrListFailureMatchesPathAndToleratesCRLF(t *testing.T) {
	if msg := stderrListFailure(listManyStderr, "/tmp/ldm/nope"); !strings.Contains(msg, "not found") {
		t.Fatalf("应取回 nope 的远端错误原文, got %q", msg)
	}
	if msg := stderrListFailure(listManyStderr, "/tmp/ldm/a"); msg != "" {
		t.Fatalf("成功路径不得命中 stderr 错误行, got %q", msg)
	}
	if msg := stderrListFailure("", "/tmp/ldm/nope"); msg != "" {
		t.Fatalf("空 stderr 应返回空, got %q", msg)
	}
}

// 兜底(D):即使 stdout 看起来列出了某目录,stderr 报它失败也必须判未知并记日志。
func TestListManyStderrMarksPathUnknown(t *testing.T) {
	var events []forward.Event
	stdout := "sftp> -ls -la \"/x\"\n-rw-r--r--    1 lan      lan 4 Sep 10 20:46 f.txt\n"
	stderr := "Can't ls: \"/x\" Permission denied\r\n"
	run := func(name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{Stdout: stdout, Stderr: stderr}, nil
	}
	c := NewCtrl(run, func(e forward.Event) { events = append(events, e) })
	got, err := c.ListMany("ai", "", []string{"/x"})
	if err != nil {
		t.Fatalf("ListMany: %v", err)
	}
	if items, ok := got["/x"]; ok {
		t.Fatalf("stderr 报失败的路径必须缺席, got %#v", items)
	}
	found := false
	for _, e := range events {
		if e.Level == "error" && strings.Contains(e.Message, "Permission denied") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error event carrying remote stderr, got %+v", events)
	}
}

// ListMany 端到端：stdout/stderr 分离（实测错误行在 stderr），
// 失败目录缺席且写入带远端原文的 error 事件；成功/空目录正确归属。
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
		return osutil.Outcome{Stdout: listManyFixture, Stderr: listManyStderr}, nil
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
	if items, ok := got["/tmp/ldm/nope"]; ok {
		t.Fatalf("失败目录必须缺席(未知), 得到 %#v", items)
	}
	if items, ok := got["/tmp/ldm/empty"]; !ok || len(items) != 0 {
		t.Fatalf("空目录必须在场且为空, got %#v ok=%v", items, ok)
	}
	if len(events) < 3 {
		t.Fatalf("expected start+error+done events, got %+v", events)
	}
	if events[0].SourceType != "sftp" || events[0].SourceID != "ai" || events[0].Message != "sftp ls many (3 dirs)" {
		t.Fatalf("unexpected start event: %+v", events[0])
	}
	foundErr := false
	for _, e := range events {
		if e.Level == "error" && strings.Contains(e.Message, "/tmp/ldm/nope") && strings.Contains(e.Message, "not found") {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatalf("expected error event carrying remote stderr for nope, got %+v", events)
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
