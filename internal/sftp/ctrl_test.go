package sftp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// fakeRunner records every (name, args) invocation so tests can assert on the
// exact ssh/sftp argument list. It also creates the ControlPath socket file on
// demand so Connect's post-spawn poll succeeds without a real ssh master.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []runnerCall
	batches [][]byte         // 每次批处理的实际内容（从 -b 指向的临时文件读回）
	queued  []osutil.Outcome // 非空时按序弹出，用于喂 ls 输出
}

type runnerCall struct {
	name string
	args []string
}

func (f *fakeRunner) push(o osutil.Outcome) {
	f.mu.Lock()
	f.queued = append(f.queued, o)
	f.mu.Unlock()
}

func (f *fakeRunner) run(name string, args ...string) (osutil.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{name: name, args: args})
	if i := indexOf(args, "-b"); i >= 0 && i+1 < len(args) {
		if b, err := os.ReadFile(args[i+1]); err == nil {
			f.batches = append(f.batches, b)
		}
	}
	var out osutil.Outcome
	if len(f.queued) > 0 {
		out = f.queued[0]
		f.queued = f.queued[1:]
	}
	f.mu.Unlock()
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			_ = os.WriteFile(strings.TrimPrefix(a, "ControlPath="), nil, 0600)
		}
	}
	return out, nil
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildBatchAndWrite(t *testing.T) {
	c := NewBatchBackend(nil, nil)
	dir := t.TempDir()
	b, err := c.buildBatch("get", "/remote/path", "/local/path")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `get "/remote/path" "/local/path"`+"\n" {
		t.Fatalf("get batch wrong: %q", got)
	}
	p, err := c.writeBatch(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 无 chmod 语义（权限由 ACL 决定），跳过权限断言。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("temp file should be 0600, got %o", info.Mode().Perm())
	}
	data, _ := os.ReadFile(p)
	if string(data) != string(b) {
		t.Fatalf("file content mismatch")
	}
}

func TestBuildBatchQuoting(t *testing.T) {
	c := NewBatchBackend(nil, nil)
	cases := []struct {
		op, remote, local, want string
	}{
		{"ls", "/remote/dir with spaces", "", `ls -la "/remote/dir with spaces"` + "\n"},
		{"getr", "/remote/my dir", "/local/dst/my dir", `get -r "/remote/my dir" "/local/dst/my dir"` + "\n"},
		{"get", "/remote/a\"b", "/local/c\\d", `get "/remote/a\"b" "/local/c\\d"` + "\n"},
		{"put", "/remote/path", `C:\Users\foo bar\file.txt`, `put "C:\\Users\\foo bar\\file.txt" "/remote/path"` + "\n"},
		{"rm", "/remote/x", "", `rm "/remote/x"` + "\n"},
		{"mkdir", "/remote/new dir", "", `mkdir "/remote/new dir"` + "\n"},
		{"rename", "/remote/old name", "/remote/new\"name", `rename "/remote/old name" "/remote/new\"name"` + "\n"},
	}
	for _, tc := range cases {
		b, err := c.buildBatch(tc.op, tc.remote, tc.local)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.op, err)
		}
		if got := string(b); got != tc.want {
			t.Fatalf("%s batch wrong: got %q want %q", tc.op, got, tc.want)
		}
	}
}

func TestBuildBatchRejectsControlChars(t *testing.T) {
	c := NewBatchBackend(nil, nil)
	bad := []string{"/remote/evil\n!calc", "/remote/evil\r!calc", "/remote/evil\x00x"}
	ops := []struct {
		op, remote, local string
	}{
		{"ls", "", ""},
		{"get", "", "/local/ok"},
		{"put", "", "/local/ok"},
		{"rm", "", ""},
		{"mkdir", "", ""},
		{"rename", "", "/remote/ok"},
	}
	for _, op := range ops {
		for _, p := range bad {
			remote := op.remote
			if remote == "" {
				remote = p
			}
			_, err := c.buildBatch(op.op, remote, op.local)
			if err == nil {
				t.Fatalf("%s should reject control char in remote path %q", op.op, p)
			}
			if !strings.Contains(err.Error(), "控制字符") {
				t.Fatalf("%s error should mention 控制字符, got %v", op.op, err)
			}
		}
	}
	for _, p := range bad {
		if _, err := c.buildBatch("get", "/remote/ok", p); err == nil {
			t.Fatalf("get should reject control char in local path %q", p)
		}
		if _, err := c.buildBatch("put", "/remote/ok", p); err == nil {
			t.Fatalf("put should reject control char in local path %q", p)
		}
		if _, err := c.buildBatch("rename", "/remote/ok", p); err == nil {
			t.Fatalf("rename should reject control char in new path %q", p)
		}
	}
	if b, err := c.buildBatch("nope", "/a", "/b"); err != nil || b != nil {
		t.Fatalf("unknown op should return nil,nil; got %q,%v", b, err)
	}
}

func TestConnectArgsIncludeConnectTimeout(t *testing.T) {
	if isWindows {
		t.Skip("ControlMaster connect path is unix-only")
	}
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	if err := c.Connect("myhost", ""); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "ssh" {
		t.Fatalf("want ssh, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("ssh args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestRunArgsIncludeConnectTimeout(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	if _, err := c.run("myhost", "", []byte("ls -l \"/\"\n")); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "sftp" {
		t.Fatalf("want sftp, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("sftp args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestDisconnectArgsIncludeConnectTimeout(t *testing.T) {
	if isWindows {
		t.Skip("Windows is per-command mode; no ssh -O exit call")
	}
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	if err := c.Disconnect("myhost"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if len(fr.calls) == 0 {
		t.Fatal("runner not invoked")
	}
	if fr.calls[0].name != "ssh" {
		t.Fatalf("want ssh, got %q", fr.calls[0].name)
	}
	if !hasArg(fr.calls[0].args, "-O") || !hasArg(fr.calls[0].args, "exit") {
		t.Fatalf("want -O exit, got %v", fr.calls[0].args)
	}
	if !hasArg(fr.calls[0].args, "ConnectTimeout=10") {
		t.Fatalf("ssh -O exit args missing ConnectTimeout=10: %v", fr.calls[0].args)
	}
}

func TestCommandErr(t *testing.T) {
	if got := commandErr(osutil.Outcome{Stderr: "  Host key verification failed.  ", ExitCode: 255}); got != "Host key verification failed." {
		t.Fatalf("stderr trimmed wrong: %q", got)
	}
	// stderr empty falls back to stdout
	if got := commandErr(osutil.Outcome{Stdout: "Permission denied (publickey).", ExitCode: 255}); got != "Permission denied (publickey)." {
		t.Fatalf("stdout fallback wrong: %q", got)
	}
	// both empty -> exit code
	if got := commandErr(osutil.Outcome{ExitCode: 255}); got != "exit 255" {
		t.Fatalf("exit fallback wrong: %q", got)
	}
}

// TestGetSurfacesStderr verifies the fix: when sftp exits non-zero, the
// returned error must carry the real stderr (e.g. "File ... not found."),
// not a bare "exit status 1".
func TestGetSurfacesStderr(t *testing.T) {
	failRunner := func(name string, args ...string) (osutil.Outcome, error) {
		// Simulate `sftp` exiting 1 with a diagnostic on stderr.
		return osutil.Outcome{Stderr: `File "/remote/nope.txt" not found.`, ExitCode: 1}, nil
	}
	c := NewBatchBackend(failRunner, nil)
	err := c.Get("myhost", "", "/remote/nope.txt", "/local/nope.txt")
	if err == nil {
		t.Fatal("expected error for failed get")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should carry stderr detail, got: %v", err)
	}
	if strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("error should not be a bare exit status, got: %v", err)
	}

	// Recursive directory download failure likewise surfaces stderr.
	err = c.GetRecursive("myhost", "", "/remote/dir", "/local/dir")
	if err == nil {
		t.Fatal("expected error for failed recursive get")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("recursive error should carry stderr detail, got: %v", err)
	}
}

// SFTP 操作必须产生开始/完成事件(source_type=sftp, source_id=host),
// 供日志面板展示——否则 SFTP 视图的日志面板形同虚设。
func TestSftpEventsEmitted(t *testing.T) {
	var mu sync.Mutex
	var events []forward.Event
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, func(e forward.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	if _, err := c.List("ai", "", "/home/lan"); err != nil {
		t.Fatalf("list: %v", err)
	}
	mu.Lock()
	got := append([]forward.Event(nil), events...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("expected start+done events, got %+v", got)
	}
	first := got[0]
	if first.SourceType != "sftp" || first.SourceID != "ai" || first.Level != "info" || first.Message != "sftp ls /home/lan" {
		t.Fatalf("unexpected first event: %+v", first)
	}
	last := got[len(got)-1]
	if last.Level != "info" || !strings.HasPrefix(last.Message, "sftp ls done") {
		t.Fatalf("expected done event, got %+v", last)
	}
}

// 失败事件必须携带 sftp/ssh 的真实 stderr(如 Permission denied),
// 而不是裸退出码。
func TestSftpFailedEventCarriesStderr(t *testing.T) {
	var events []forward.Event
	c := NewBatchBackend(func(name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 1, Stderr: "Permission denied (publickey)"}, nil
	}, func(e forward.Event) { events = append(events, e) })
	if err := c.Get("ai", "", "/r/f", "/l/f"); err == nil {
		t.Fatal("expected error")
	}
	if len(events) < 2 {
		t.Fatalf("expected start+error events, got %+v", events)
	}
	last := events[len(events)-1]
	if last.Level != "error" || !strings.Contains(last.Message, "Permission denied") {
		t.Fatalf("expected error with stderr detail, got %+v", last)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:                  "0 B",
		1023:               "1023 B",
		2048:               "2.0 KB",
		1024 * 1024:        "1.0 MB",
		1536 * 1024:        "1.5 MB",
		1024 * 1024 * 1024: "1.0 GB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Fatalf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// 路线 B(增强日志):put 的开始/完成日志携带本地文件大小。
func TestPutLogsCarrySize(t *testing.T) {
	var events []forward.Event
	dir := t.TempDir()
	local := filepath.Join(dir, "upload.bin")
	if err := os.WriteFile(local, make([]byte, 2048), 0600); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, func(e forward.Event) { events = append(events, e) })
	if err := c.Put("ai", "", local, "/remote/upload.bin"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected start+done events, got %+v", events)
	}
	if !strings.Contains(events[0].Message, "(2.0 KB)") {
		t.Fatalf("start should carry size, got %q", events[0].Message)
	}
	last := events[len(events)-1]
	if !strings.Contains(last.Message, "(2.0 KB)") {
		t.Fatalf("done should carry size, got %q", last.Message)
	}
}

// socket 路径必须随 user 变化：同 host 异 user 绝不能共用 master。
func TestControlPathKeyedByUser(t *testing.T) {
	c := NewBatchBackend(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	a := c.controlPathFor("prod-01", "")
	b := c.controlPathFor("prod-01", "alice")
	if a == b {
		t.Fatalf("同 host 异 user 必须得到不同 socket: %q", a)
	}
}

// Disconnect/Connected 只拿到 host，必须用内部记住的 user 反查同一个 socket。
func TestRememberedUserUsedByHostOnlyAPI(t *testing.T) {
	c := NewBatchBackend(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	c.rememberUser("prod-01", "alice")
	if got, want := c.controlPathFor("prod-01", c.userFor("prod-01")), c.controlPathFor("prod-01", "alice"); got != want {
		t.Fatalf("host-only API 未复用记住的 user: %q vs %q", got, want)
	}
}

// socket 路径必须与 sshconn 的唯一来源逐字一致：两包各算一遍必然漂移，
// 一旦漂移 sftp 就永远复用不到 sshconn 建的 master。
func TestControlPathMatchesSSHConn(t *testing.T) {
	c := NewBatchBackend(nil, nil)
	cases := []struct{ host, user string }{
		{"prod-01", ""},
		{"prod-01", "alice"},
		{"host with space", "u/ser+plus"},
	}
	for _, tc := range cases {
		if got, want := c.controlPathFor(tc.host, tc.user), sshconn.ControlPath(tc.host, tc.user); got != want {
			t.Fatalf("controlPathFor(%q,%q)=%q, sshconn.ControlPath=%q", tc.host, tc.user, got, want)
		}
	}
}

// Connected/Disconnect 只拿到 host：必须用记住的 user 命中同一个 socket；
// 未知 host 回退空 user（与旧版 cm-<host>.sock 路径一致）。
func TestHostOnlyAPIsUseRememberedUserSocket(t *testing.T) {
	if isWindows {
		t.Skip("ControlMaster socket path is unix-only")
	}
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	host, user := "prod-01", "alice"
	if err := os.WriteFile(sshconn.ControlPath(host, user), nil, 0600); err != nil {
		t.Fatalf("预置 socket 失败: %v", err)
	}
	if c.Connected(host) {
		t.Fatal("未记住 user 前不应命中 alice 的 socket")
	}
	c.rememberUser(host, user)
	if !c.Connected(host) {
		t.Fatal("记住 user 后应命中同一 socket")
	}
	if err := c.Disconnect(host); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	want := "ControlPath=" + sshconn.ControlPath(host, user)
	if len(fr.calls) == 0 || !hasArg(fr.calls[0].args, want) {
		t.Fatalf("Disconnect 未作用在记住的 user socket 上: %v", fr.calls)
	}
	if c.Connected("never-seen") {
		t.Fatal("未知 host 的回退路径不应存在")
	}
}

// mockLs 生成**真实形状**的批处理输出：每段以 'sftp> -ls -la "<path>"' 回显开头。
// parseListMany 正是靠这个前缀切块并**按发送顺序**归属路径；缺了它整块会被丢弃，
// 测试会得到'目录未知'而不是'列出了内容'（见 internal/sftp/listmany_parse.go:52-76）。
// 另：ctrl_test.go 的 import 需补 "fmt"。
func mockLs(path string, lines ...string) string {
	out := "sftp> -ls -la \"" + path + "\"\n"
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func lsLine(name string, isDir bool, size int64) string {
	kind := "-"
	if isDir {
		kind = "d"
	}
	return fmt.Sprintf("%srw-r--r-- 1 u g %d Jan  1 00:00 %s", kind, size, name)
}

func TestRemoveRecursiveBuildsDashPrefixedBatch(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	// 第 1 批：列 /root → 1 个文件 + 1 个子目录
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("a.txt", false, 10), lsLine("sub", true, 0))})
	// 第 2 批：列 /root/sub → 回显在场、内容为空 ⇒ 存在但为空的目录
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/sub")})
	// 第 3 批：删除批
	fr.push(osutil.Outcome{ExitCode: 0})

	if err := c.RemoveRecursive("h", "", "/root"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.calls) != 3 {
		t.Fatalf("want 3 runner calls (list root, list sub, delete), got %d", len(fr.calls))
	}
	bat := fr.batches[len(fr.batches)-1]
	want := "-rm \"/root/a.txt\"\n-rmdir \"/root/sub\"\n-rmdir \"/root\"\n"
	if string(bat) != want {
		t.Fatalf("delete batch = %q, want %q", bat, want)
	}
}

func TestRemoveRecursiveSingleFileOnlyRm(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	// sftp ls -l <file> 返回它自己，且 Item.Name 可能是完整路径。
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/a.txt", lsLine("/root/a.txt", false, 10))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/root/a.txt"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/root/a.txt\"\n"; got != want {
		t.Fatalf("single-file batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursiveEmptyDirOnlyRmdir(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/empty")})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/empty"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rmdir \"/empty\"\n"; got != want {
		t.Fatalf("empty dir batch = %q, want %q", got, want)
	}
}

// 实测形状 1：'-rm' 失败**不带引号**，且 '-' 前缀下 EXIT 恒为 0。
// 一次性 sshd + internal-sftp 实测原文： remote delete /tmp/.../f.txt: Permission denied
// （旧夹具喂的 Can't rm: "<path>": ... 在实测中并不存在，已替换。）
func TestRemoveRecursiveRmFailureIsReported(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("a.txt", false, 10))})
	fr.push(osutil.Outcome{ExitCode: 0, Stderr: "remote delete /root/a.txt: Permission denied\r\n"})
	err := c.RemoveRecursive("h", "", "/root")
	if err == nil || !strings.Contains(err.Error(), "/root/a.txt") {
		t.Fatalf("'-rm' 部分失败必须报错并带路径, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "/root:") {
		t.Fatalf("只应报告真正失败的 /root/a.txt，不得把 /root 也算上: %v", err)
	}
}

// 实测形状 2：'-rmdir' 失败**自带双引号**： remote rmdir "<path>": Failure
func TestRemoveRecursiveRmdirFailureIsReported(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("sub", true, 0))})
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/sub")})
	fr.push(osutil.Outcome{ExitCode: 0, Stderr: "remote rmdir \"/root/sub\": Failure\r\n"})
	err := c.RemoveRecursive("h", "", "/root")
	if err == nil || !strings.Contains(err.Error(), "/root/sub") {
		t.Fatalf("'-rmdir' 部分失败必须报错并带路径, got %v", err)
	}
}

// 反模糊匹配：stderr 提到的是**别的**路径时，绝不能把请求路径判成失败
// （/root/a.txt 不得命中 /root/a.txt.bak 或 "/root/a.txtX"）。
func TestRemoveRecursiveFailureMatchingIsPathExact(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("a.txt", false, 10))})
	fr.push(osutil.Outcome{ExitCode: 0, Stderr: "remote delete /root/a.txt.bak: Permission denied\r\n" +
		"remote rmdir \"/root/a.txtX\": Failure\r\n"})
	if err := c.RemoveRecursive("h", "", "/root"); err != nil {
		t.Fatalf("无关路径的 stderr 不得命中请求路径: %v", err)
	}
}

// 目录目标内部恰有一个**同名非目录子项**：目录参数的列表返回 basename（"foo" != "/root/foo"），
// 因此不得走单文件特判（否则只发 -rm 目录、漏删整棵子树），必须正常 BFS。
func TestRemoveRecursiveSameNameChildIsNotSingleFile(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/foo", lsLine("foo", false, 1))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/root/foo"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/root/foo/foo\"\n-rmdir \"/root/foo\"\n"; got != want {
		t.Fatalf("目录目标不得走单文件特判: batch = %q, want %q", got, want)
	}
}

// 文件目标：OpenSSH 对文件参数返回**完整远端路径**，此时才走单文件特判（只发 -rm）。
// 与上面同名子项用例成对，锁死 items[0].Name == path 这一判别依据。
func TestRemoveRecursiveFileTargetUsesFullPath(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root/a.txt", lsLine("/root/a.txt", false, 10))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/root/a.txt"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.calls) != 2 {
		t.Fatalf("文件目标应只有 列举+删除 两次调用, got %d", len(fr.calls))
	}
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/root/a.txt\"\n"; got != want {
		t.Fatalf("文件目标 batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursivePathWithSpace(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/my dir", lsLine("a b.txt", false, 1))})
	fr.push(osutil.Outcome{ExitCode: 0})
	if err := c.RemoveRecursive("h", "", "/my dir"); err != nil {
		t.Fatalf("RemoveRecursive: %v", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if got, want := string(fr.batches[len(fr.batches)-1]), "-rm \"/my dir/a b.txt\"\n-rmdir \"/my dir\"\n"; got != want {
		t.Fatalf("space path batch = %q, want %q", got, want)
	}
}

func TestRemoveRecursiveRejectsGlobInChild(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/root", lsLine("lit*name.txt", false, 1))})
	if err := c.RemoveRecursive("h", "", "/root"); err == nil {
		t.Fatal("子项名含通配符必须拒绝整次递归删除")
	}
	if len(fr.calls) != 1 { // 只应有列举调用，绝无删除批
		t.Fatalf("must not run delete batch, calls=%d", len(fr.calls))
	}
}

func TestRemoveRecursiveRejectsGlobPaths(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	if err := c.RemoveRecursive("h", "", "/root/lit*name.txt"); err == nil {
		t.Fatal("expected error for glob metachar path")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("must not spawn any process, got %d calls", len(fr.calls))
	}
}

// TestBuildBatchPutRecursive（2026-10-10 契约统一，RED→GREEN）：递归上传的批处理脚本必须
// 逐文件 `put` 到**最终路径** `<remoteDir>/<相对源根>`，而不是旧的 `put -r <local> <remoteDir>`
// —— OpenSSH 的 put -r 把 remoteDir 当父目录、再追加源目录名，正是「多套一层」的来源。
// 目标目录（含空目录）先逐级 `-mkdir`：`-b` 模式下未加 `-` 前缀的命令失败会中止整批，
// 而「目录已存在」是并入语义下必须容忍的（与 RemoveRecursive 的 -rm/-rmdir 同一手法）。
func TestBuildBatchPutRecursive(t *testing.T) {
	src := t.TempDir()
	makeLocalTree(t, src, map[string]string{"x.txt": "X", "sub/y.txt": "Y"})
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewBatchBackend(nil, nil)
	b, err := c.buildBatch("putr", "/remote/dir", src)
	if err != nil {
		t.Fatal(err)
	}
	// 实现会先把源根 EvalSymlinks 再遍历（软链根必须能上传），脚本里的本地路径因此是解析后
	// 的真实路径（Windows 上 EvalSymlinks 可能改变大小写）—— 期望值同样按解析后的路径构造。
	root, rerr := filepath.EvalSymlinks(src)
	if rerr != nil {
		t.Fatal(rerr)
	}
	q := func(p string) string {
		t.Helper()
		s, qerr := quoteArg(p)
		if qerr != nil {
			t.Fatal(qerr)
		}
		return s
	}
	// 目录行按 `-mkdir` 的生成顺序：先是远端目标的**全部祖先**自上而下（OpenSSH 的 mkdir
	// 是单级语义，只 -mkdir 叶子在父目录缺失时会静默失败、随后 put 让整批失败），
	// 再按 WalkDir 的字典序补源树里的每个子目录（父先于子），最后是逐文件 put。
	want := strings.Join([]string{
		`-mkdir "/remote"`,
		`-mkdir "/remote/dir"`,
		`-mkdir "/remote/dir/empty"`,
		`-mkdir "/remote/dir/sub"`,
		"put " + q(filepath.Join(root, "sub", "y.txt")) + ` "/remote/dir/sub/y.txt"`,
		"put " + q(filepath.Join(root, "x.txt")) + ` "/remote/dir/x.txt"`,
	}, "\n") + "\n"
	if got := string(b); got != want {
		t.Fatalf("putr batch =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(string(b), "put -r ") {
		t.Fatalf("递归上传不得再走 put -r（父目录约定会多套一层）: %q", string(b))
	}
}

// TestBuildBatchPutRecursiveCreatesMissingAncestors（2026-10-10 复核轮 2 / Minor-2，RED→GREEN）：
// 目标目录的**中间层**可能不存在（GoBackend 用 MkdirAll 自己补；batch 只 -mkdir 叶子会整批
// 失败）。批处理必须对目标路径自上而下逐级 `-mkdir`，且每级都带 `-` 前缀 —— `-mkdir` 对
// **已存在**的目录也报 "Failure"，带前缀时该失败被抑制、EXIT 仍为 0，所以逐级建天然幂等
// （真机实测见 e2e/PROBE，用例侧证据见 TestTreeE2E 的「祖先缺失」阶段）。
func TestBuildBatchPutRecursiveCreatesMissingAncestors(t *testing.T) {
	for _, tc := range []struct {
		remoteDir string
		want      []string
	}{
		{"/remote/deep/nested/dir", []string{`-mkdir "/remote"`, `-mkdir "/remote/deep"`, `-mkdir "/remote/deep/nested"`, `-mkdir "/remote/deep/nested/dir"`}},
		{"rel/dir", []string{`-mkdir "rel"`, `-mkdir "rel/dir"`}},
		{".", nil}, // 当前目录一定存在：不产生任何 -mkdir 行
	} {
		src := t.TempDir()
		makeLocalTree(t, src, map[string]string{"x.txt": "X"})
		c := NewBatchBackend(nil, nil)
		b, err := c.buildBatch("putr", tc.remoteDir, src)
		if err != nil {
			t.Fatalf("buildBatch(%q): %v", tc.remoteDir, err)
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) < len(tc.want)+1 {
			t.Fatalf("remoteDir=%q 脚本过短: %q", tc.remoteDir, string(b))
		}
		for i, want := range tc.want {
			if lines[i] != want {
				t.Fatalf("remoteDir=%q 第 %d 行 = %q, want %q（必须自上而下逐级建祖先）\n脚本=%q", tc.remoteDir, i, lines[i], want, string(b))
			}
		}
		// 目录行必须全部带 `-` 前缀，且都排在 put 之前。
		seenPut := false
		for _, ln := range lines {
			if strings.HasPrefix(ln, "put ") {
				seenPut = true
				continue
			}
			if strings.Contains(ln, "mkdir") && !strings.HasPrefix(ln, "-mkdir ") {
				t.Fatalf("remoteDir=%q 的 mkdir 行必须带 `-` 前缀（已存在要容忍）: %q", tc.remoteDir, ln)
			}
			if seenPut {
				t.Fatalf("remoteDir=%q 的目录行必须排在所有 put 之前: %q", tc.remoteDir, ln)
			}
		}
	}
}

func TestPutRecursiveRunsSftp(t *testing.T) {
	src := t.TempDir()
	makeLocalTree(t, src, map[string]string{"x.txt": "X"})
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	if err := c.PutRecursive("h", "", src, "/remote/dir"); err != nil {
		t.Fatalf("PutRecursive: %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].name != "sftp" {
		t.Fatalf("calls = %+v", fr.calls)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.batches) != 1 {
		t.Fatalf("必须恰好一条批处理脚本, got %d", len(fr.batches))
	}
	if got := string(fr.batches[0]); !strings.Contains(got, `"/remote/dir/x.txt"`) || !strings.Contains(got, "-mkdir ") {
		t.Fatalf("批处理脚本必须建目录 + 逐文件 put 到最终路径, got %q", got)
	}
}

// —— Task 5 门面委派（自审 S11 的回归护栏）——

// fakeBackend 是 Backend 的测试替身：只记录门面是否把调用委派进来、
// 以及委派时的 TransferRequest 取值（尤其是 Atomic）。
type fakeBackend struct {
	lastReq TransferRequest
	called  string

	// Task 9：Cancel/AtomicCapable 的委派观测点。
	lastCancelID string
	cancelOK     bool
	atomicOK     bool
}

func (f *fakeBackend) List(string, string, string) ([]Item, error) {
	f.called = "List"
	return nil, nil
}
func (f *fakeBackend) ListMany(string, string, []string) (map[string][]Item, error) {
	f.called = "ListMany"
	return nil, nil
}
func (f *fakeBackend) Home(string, string) (string, error) { f.called = "Home"; return "", nil }
func (f *fakeBackend) TransferGet(req TransferRequest, _ func(Progress)) error {
	f.called, f.lastReq = "TransferGet", req
	return nil
}
func (f *fakeBackend) TransferGetTree(TransferRequest, func(Progress)) error {
	f.called = "TransferGetTree"
	return nil
}
func (f *fakeBackend) TransferPut(req TransferRequest, _ func(Progress)) error {
	f.called, f.lastReq = "TransferPut", req
	return nil
}
func (f *fakeBackend) TransferPutTree(TransferRequest, func(Progress)) error {
	f.called = "TransferPutTree"
	return nil
}
func (f *fakeBackend) Remove(string, string, string) error { f.called = "Remove"; return nil }
func (f *fakeBackend) RemoveRecursive(string, string, string) error {
	f.called = "RemoveRecursive"
	return nil
}
func (f *fakeBackend) Mkdir(string, string, string) error          { f.called = "Mkdir"; return nil }
func (f *fakeBackend) Rename(string, string, string, string) error { f.called = "Rename"; return nil }
func (f *fakeBackend) Connect(string, string) error                { f.called = "Connect"; return nil }
func (f *fakeBackend) Search(context.Context, string, string, string, string, int, int, func(int)) (SearchOutcome, error) {
	f.called = "Search"
	return SearchOutcome{}, nil
}
func (f *fakeBackend) Connected(string) bool   { f.called = "Connected"; return true }
func (f *fakeBackend) Disconnect(string) error { f.called = "Disconnect"; return nil }
func (f *fakeBackend) CloseAll()               { f.called = "CloseAll" }
func (f *fakeBackend) Cancel(id string) bool {
	f.called, f.lastCancelID = "Cancel", id
	return f.cancelOK
}
func (f *fakeBackend) AtomicCapable() bool { return f.atomicOK }

// TestFacadeLegacyFaceUsesSelectedBackendWithAtomicFalse 钉住门面契约：
// legacy 四参面必须经 backend() 委派（而不是硬绑 batch），且 Atomic=false（直写目标，
// sync 自带 .part+rename）。这样 Task 6 接上 resolveTransport 后，
// SSHORE_SFTP_TRANSPORT=gosftp 才能真正改变 internal/sync 走的后端。
func TestFacadeLegacyFaceUsesSelectedBackendWithAtomicFalse(t *testing.T) {
	fb := &fakeBackend{}
	c := &Ctrl{sel: func() string { return "gosftp" }, forced: fb}

	if err := c.Get("h", "u", "/r", "/l"); err != nil {
		t.Fatal(err)
	}
	if fb.called != "TransferGet" || fb.lastReq.Atomic {
		t.Fatalf("legacy Get 必须走 backend() 且 Atomic=false, called=%q atomic=%v", fb.called, fb.lastReq.Atomic)
	}
	if fb.lastReq.Host != "h" || fb.lastReq.User != "u" || fb.lastReq.Remote != "/r" || fb.lastReq.Local != "/l" {
		t.Fatalf("legacy 四参应逐字映射进 TransferRequest, got %+v", fb.lastReq)
	}

	// Put 的参数序与 Get 不同（local,remote），单独钉一次。
	if err := c.Put("h", "u", "/loc", "/rem"); err != nil {
		t.Fatal(err)
	}
	if fb.called != "TransferPut" || fb.lastReq.Local != "/loc" || fb.lastReq.Remote != "/rem" {
		t.Fatalf("Put 应映射为 TransferPut{Local:/loc, Remote:/rem}, called=%q req=%+v", fb.called, fb.lastReq)
	}

	if err := c.GetRecursive("h", "u", "/r", "/l"); err != nil {
		t.Fatal(err)
	}
	if fb.called != "TransferGetTree" {
		t.Fatalf("GetRecursive 应委派 TransferGetTree, called=%q", fb.called)
	}
	if err := c.PutRecursive("h", "u", "/loc", "/rdir"); err != nil {
		t.Fatal(err)
	}
	if fb.called != "TransferPutTree" {
		t.Fatalf("PutRecursive 应委派 TransferPutTree, called=%q", fb.called)
	}
}

// TestFacadeCancelDelegatesTheID 钉住门面 Cancel（Task 9）：必须把 id 原样转给后端，
// 并把后端的 bool 结果原样上抛（未知/已完成 id ⇒ false）。绝不能在门面里吞掉。
func TestFacadeCancelDelegatesTheID(t *testing.T) {
	fb := &fakeBackend{cancelOK: true}
	c := &Ctrl{forced: fb}
	if !c.Cancel("t-9") {
		t.Fatal("后端说取消成功时门面必须返回 true")
	}
	if fb.called != "Cancel" || fb.lastCancelID != "t-9" {
		t.Fatalf("Cancel 必须把 id 原样转给后端: called=%q id=%q", fb.called, fb.lastCancelID)
	}

	fb2 := &fakeBackend{cancelOK: false}
	c2 := &Ctrl{forced: fb2}
	if c2.Cancel("t-unknown") {
		t.Fatal("后端说未知 id 时门面必须返回 false（幂等语义）")
	}
}

// TestFacadeAtomicCapableFollowsBackend 钉住能力访问器：门面必须回答「当前选中的后端
// 是否支持原子提交」，而不是写死。绑定层据此决定 TransferRequest.Atomic。
func TestFacadeAtomicCapableFollowsBackend(t *testing.T) {
	if (&Ctrl{forced: &fakeBackend{atomicOK: false}}).AtomicCapable() {
		t.Fatal("后端声明不支持时门面必须返回 false")
	}
	if !(&Ctrl{forced: &fakeBackend{atomicOK: true}}).AtomicCapable() {
		t.Fatal("后端声明支持时门面必须返回 true")
	}
}

// TestNewCtrlFacadeWiring 钉住两个构造：NewCtrl 不注入 selector（测试/兼容路径），
// NewCtrlWith 额外注入懒解析选择器（生产路径）。
// Task 5 评审 I1：这里必须**显式清掉 SSHORE_SFTP_TRANSPORT**，否则测试结果取
// 决于外部环境；并且各条分支都要断言：内置默认（Task 16 起为 gosftp）、显式 batch、
// 显式 gosftp，以及 env 覆盖 config。
//
// Task 16：内置默认已从 batch 切到 gosftp，因此「NewCtrl 默认拿到 batch」不再是事实 ——
// 这里改成先显式钉住内置默认，再显式选 batch 验证兼容路径。
func TestNewCtrlFacadeWiring(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	c := NewCtrl(nil, nil)
	if c.sel != nil {
		t.Fatal("NewCtrl 不得注入 selector（保持 spec D3 两参语义）")
	}
	if c.TransportKind() != KindGo {
		t.Fatalf("Task 16 内置默认应解析为 KindGo, got %v", c.TransportKind())
	}
	if _, ok := c.backend().(*GoBackend); !ok {
		t.Fatalf("NewCtrl 的 backend() 应与内置默认一致（*GoBackend）, got %T", c.backend())
	}

	// 显式选 batch（回退路径）：env 覆盖内置默认。
	t.Setenv("SSHORE_SFTP_TRANSPORT", "batch")
	cb := NewCtrl(nil, nil)
	if _, ok := cb.backend().(*BatchBackend); !ok {
		t.Fatalf("env=batch 应返回 *BatchBackend, got %T", cb.backend())
	}
	if cb.TransportKind() != KindBatch {
		t.Fatalf("env=batch 应解析为 KindBatch, got %v", cb.TransportKind())
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")

	c2 := NewCtrlWith(nil, nil, func() string { return "gosftp" })
	if c2.sel == nil || c2.sel() != "gosftp" {
		t.Fatalf("NewCtrlWith 应保留注入的 selector, got %v", c2.sel)
	}
	if c2.TransportKind() != KindGo {
		t.Fatalf("显式 gosftp 应解析为 KindGo, got %v", c2.TransportKind())
	}
	gb, ok := c2.backend().(*GoBackend)
	if !ok {
		t.Fatalf("显式选定 gosftp 时 backend() 应返回 *GoBackend, got %T", c2.backend())
	}
	if c2.backend() != Backend(gb) {
		t.Fatal("GoBackend 必须懒构造后缓存，而不是每次新建")
	}

	// env 覆盖 config：即便 selector 说 batch，env=gosftp 也要选中 GoBackend。
	t.Setenv("SSHORE_SFTP_TRANSPORT", "gosftp")
	c3 := NewCtrlWith(nil, nil, func() string { return "batch" })
	if _, ok := c3.backend().(*GoBackend); !ok {
		t.Fatalf("env=gosftp 必须覆盖 config=batch, got %T", c3.backend())
	}

	// 显式选 batch：即使 env 清空也走 batch 分支。
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	c4 := NewCtrlWith(nil, nil, func() string { return "batch" })
	if _, ok := c4.backend().(*BatchBackend); !ok {
		t.Fatalf("显式 batch 应返回 *BatchBackend, got %T", c4.backend())
	}
}

// TestBatchRejectsAtomicTransfer 钉住评审 M4 的行为裁决：新面调用方对 batch 后端
// 传 Atomic=true 时**显式报错**，绝不静默退化为直写目标（那会让调用方误以为
// 拿到了 .part + 提交的原子保证）。Atomic=false 仍照旧直写。
func TestBatchRejectsAtomicTransfer(t *testing.T) {
	fr := &fakeRunner{}
	c := NewBatchBackend(fr.run, nil)
	atomic := TransferRequest{Host: "h", User: "", Remote: "/r", Local: "/l", Atomic: true}
	cases := []struct {
		name string
		run  func() error
	}{
		{"TransferGet", func() error { return c.TransferGet(atomic, nil) }},
		{"TransferGetTree", func() error { return c.TransferGetTree(atomic, nil) }},
		{"TransferPut", func() error { return c.TransferPut(atomic, nil) }},
		{"TransferPutTree", func() error { return c.TransferPutTree(atomic, nil) }},
	}
	for _, tc := range cases {
		err := tc.run()
		if err == nil {
			t.Fatalf("%s: Atomic=true 必须报错（否则就是静默退化）", tc.name)
		}
		if !errors.Is(err, errBatchAtomic) {
			t.Fatalf("%s: 错误应可判定为 errBatchAtomic, got %v", tc.name, err)
		}
	}
	fr.mu.Lock()
	calls := len(fr.calls)
	fr.mu.Unlock()
	if calls != 0 {
		t.Fatalf("Atomic 守卫必须在使用 runner 之前拦下，实际发了 %d 条命令", calls)
	}

	// Atomic=false（legacy 面）必须继续可用。
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/r", lsLine("a.txt", false, 1))})
	if err := c.TransferGet(TransferRequest{Host: "h", Remote: "/r", Local: "/l"}, nil); err != nil {
		t.Fatalf("Atomic=false 不得被守卫拦住: %v", err)
	}
}

// —— Task 2：探活（Ctrl.Probe）——

// blockingFakeBackend 是专为「batch 探活必须共享在飞结果」造的替身（审核 I4）：
// 本文件既有的 fakeBackend 的 List 立即返回、called 字段也无锁 —— 既造不出「阻塞在飞」，
// 又会与仍在跑的 goroutine 构成 -race 报告，所以不能复用。
// 嵌入 Backend 接口占位：只覆写被走到的方法（没覆写的方法真被调用会 nil panic，
// 这是有意的，不静默通过）。
type blockingFakeBackend struct {
	Backend
	listCalls atomic.Int32
	entered   chan struct{}
	release   chan struct{}
}

func (b *blockingFakeBackend) List(string, string, string) ([]Item, error) {
	b.listCalls.Add(1)
	select {
	case <-b.entered: // 只关一次：放行后的再次调用要立刻返回，不能重复 close
	default:
		close(b.entered)
	}
	<-b.release // 阻塞到测试放行：模拟 batch 的 cmd.Run() 挂住
	return nil, errors.New("dead")
}

// TestCtrlProbeUsesPoolForGoBackend 钉住分派：*GoBackend 必须走 g.Probe → pool.Probe
// （真实 ReadDirContext(".") 往返 + probeTimeout 预算 + 慢拨号复核），而不是走 List/batch
// 回退分支 —— 分派写反会让默认 KindGo 后端丢掉 probeTimeout 与取消传播。
//
// 判别力（T2 复核 Minor）：只断言「慢拨号 + 健康 ⇒ true」没有判别力 —— GoBackend.List 也走
// 同一个池、同一次 dial、同样能列成功。故这里再加一条两条路径**结果可分**的判据：让会话的
// 服务端工作目录消失 —— pool.Probe 读 "."（相对路径，拼在工作目录上）⇒ 必失败，而 List 读
// "/"（pkg/sftp 服务端只把相对路径拼到工作目录，"/" 仍是真实文件系统根）⇒ 仍会成功。
// 于是「分派写反」必然答 true，本用例必然变红。
func TestCtrlProbeUsesPoolForGoBackend(t *testing.T) {
	root := t.TempDir()
	var dials atomic.Int32
	g := NewGoBackend(nil, nil)
	g.pool = NewPool(func(host, user string) (*Session, error) {
		dials.Add(1)
		// 拨号比探针预算慢：模拟 ConnectTimeout 吃满 probeTimeout 的真实形态。
		time.Sleep(probeTimeout + 300*time.Millisecond)
		s, _ := newPipeSession(t, host, user, root)
		return s, nil
	})
	defer g.CloseAll()

	c := NewCtrlForcedBackend(g)
	if _, ok := c.backend().(*GoBackend); !ok {
		t.Fatal("测试前提错误：forced 后端必须是 *GoBackend")
	}

	// ① 慢拨号 + 健康会话 ⇒ true（BF5：不得因 cctx 过期把健康连接判死）。
	if !c.Probe(context.Background(), "h", "u") {
		t.Fatal("*GoBackend 的探活必须走 pool.Probe：慢拨号但会话健康 ⇒ true")
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("测试前提错误：①必须只拨号一次，dials=%d", n)
	}

	// ② 判别力（可移植写法）：换一个 backend，其会话的服务端工作目录指向一个**不存在的路径**
	// —— 相对路径（探针读 "."）必失败，而绝对路径（List 读 "/"）仍成功 ⇒ 两条路径可分。
	// 不再用「删掉工作目录」那招：Windows 不允许删除仍被占用的目录，
	// 旧写法在 CI 的 windows job 上挂过（unlinkat: The process cannot access the file
	// because it is being used by another process）。pkg/sftp 的 WithServerWorkingDirectory
	// 只存字段、不做存在性校验，因此"不存在的 CWD"在两端行为一致、与平台无关。
	g2 := NewGoBackend(nil, nil)
	g2.pool = NewPool(func(host, user string) (*Session, error) {
		s, _ := newPipeSession(t, host, user, filepath.Join(root, "gone-workdir"))
		return s, nil
	})
	defer g2.CloseAll()
	c2 := NewCtrlForcedBackend(g2)
	// 先证明 List 路径在这种会话上确实会成功，否则本用例的判别力不成立、会静默退化。
	if _, err := c2.List("h", "u", "/"); err != nil {
		t.Fatalf("测试前提错误：List 路径读 \"/\" 必须成功，否则两条路径不可分: %v", err)
	}
	if c2.Probe(context.Background(), "h", "u") {
		t.Fatal("服务端工作目录不存在时读 \".\" 必失败：Probe 必须答 false；答 true 说明没走 pool.Probe")
	}

	// ③ ctx 传播：pool.Probe 用调用方的 ctx，GoBackend.List 恒用 context.Background()。
	//    已取消的 ctx ⇒ 必须 false 且不得再拨号（池已空）。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.Probe(ctx, "h", "u") {
		t.Fatal("已取消的 ctx 必须传到池并让探活答 false（c.List 无视 ctx，会重新列目录并答 true）")
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("已取消的 ctx 下 pool.Probe 必须早退、不得新建会话，dials=%d", n)
	}
}

// TestCtrlProbeFallbackIsBoundedAndWaitsForInflight 钉住规格 §8.2 / 审核 I5 + R8：
// ① legacy batch 后端没有 ctx 感知的 runner（cmd.Run() 无超时）⇒ Ctrl.Probe 必须给调用方
// 一个有上界的结果（ctx 超时即返回，不得挂死，否则前端 probing 恒 true、自愈永久静默失效）；
// ② 同一 host 的并发第二次调用必须**等同一个在飞结果**，绝不能「在飞即 false」——
// false 在前端被解释成 'lost'，会对可能健康的链路做无谓的 SftpDisconnect + 重连（违反 D14）；
// ③ 在飞条目必须在飞行结束时清除，否则该 host 此后永久「在飞」、自愈静默失效。
func TestCtrlProbeFallbackIsBoundedAndWaitsForInflight(t *testing.T) {
	b := &blockingFakeBackend{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewCtrlForcedBackend(b)

	// ① 有上界：100ms ctx 超时后必须返回 false，而不是跟着挂住。
	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel1()
	start := time.Now()
	if c.Probe(ctx1, "h", "") {
		t.Fatal("ctx 超时的 batch 探活必须返回 false")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("ctx 超时后必须立刻返回，实际耗时 %v", d)
	}
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("探活必须真的打到后端 List（不得被空转短路）")
	}

	// ② 在飞期间：第二个调用者必须等结果，不得立即答 false。
	second := make(chan bool, 1)
	go func() { second <- c.Probe(context.Background(), "h", "") }()
	select {
	case got := <-second:
		t.Fatalf("在飞期间第二个调用者不得立即返回（got %v）——「在飞即 false」会被前端当成 'lost' 做无谓 teardown", got)
	case <-time.After(300 * time.Millisecond):
	}

	// 放行：List 返回 error ⇒ 结果 false，且两个调用者共享同一次后端调用。
	close(b.release)
	if got := <-second; got {
		t.Fatal("List 失败的探活必须返回 false")
	}
	if n := b.listCalls.Load(); n != 1 {
		t.Fatalf("同 host 并发探活必须去重到一次后端调用，listCalls=%d", n)
	}

	// ③ 在飞条目必须已清除：第三次调用要真的重新打到后端（listCalls 2），
	// 而不是等一条永远不会再交付的旧结果。
	if c.Probe(context.Background(), "h", "") {
		t.Fatal("List 恒失败的探活必须返回 false")
	}
	if n := b.listCalls.Load(); n != 2 {
		t.Fatalf("在飞条目未清除 ⇒ 该 host 被永久标记「在飞」、自愈静默失效，listCalls=%d", n)
	}
}
