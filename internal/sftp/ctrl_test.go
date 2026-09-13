package sftp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// fakeRunner records every (name, args) invocation so tests can assert on the
// exact ssh/sftp argument list. It also creates the ControlPath socket file on
// demand so Connect's post-spawn poll succeeds without a real ssh master.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runnerCall
}

type runnerCall struct {
	name string
	args []string
}

func (f *fakeRunner) run(name string, args ...string) (osutil.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{name: name, args: args})
	f.mu.Unlock()
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			_ = os.WriteFile(strings.TrimPrefix(a, "ControlPath="), nil, 0600)
		}
	}
	return osutil.Outcome{ExitCode: 0}, nil
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
	c := NewCtrl(nil, nil)
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
	c := NewCtrl(nil, nil)
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
	c := NewCtrl(nil, nil)
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
	c := NewCtrl(fr.run, nil)
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
	c := NewCtrl(fr.run, nil)
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
	c := NewCtrl(fr.run, nil)
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
	c := NewCtrl(failRunner, nil)
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
	c := NewCtrl(fr.run, func(e forward.Event) {
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
	c := NewCtrl(func(name string, args ...string) (osutil.Outcome, error) {
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
	c := NewCtrl(fr.run, func(e forward.Event) { events = append(events, e) })
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
	c := NewCtrl(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	a := c.controlPathFor("prod-01", "")
	b := c.controlPathFor("prod-01", "alice")
	if a == b {
		t.Fatalf("同 host 异 user 必须得到不同 socket: %q", a)
	}
}

// Disconnect/Connected 只拿到 host，必须用内部记住的 user 反查同一个 socket。
func TestRememberedUserUsedByHostOnlyAPI(t *testing.T) {
	c := NewCtrl(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	c.rememberUser("prod-01", "alice")
	if got, want := c.controlPathFor("prod-01", c.userFor("prod-01")), c.controlPathFor("prod-01", "alice"); got != want {
		t.Fatalf("host-only API 未复用记住的 user: %q vs %q", got, want)
	}
}

// socket 路径必须与 sshconn 的唯一来源逐字一致：两包各算一遍必然漂移，
// 一旦漂移 sftp 就永远复用不到 sshconn 建的 master。
func TestControlPathMatchesSSHConn(t *testing.T) {
	c := NewCtrl(nil, nil)
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
	c := NewCtrl(fr.run, nil)
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
