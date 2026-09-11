package sftp

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// isWindows: Windows OpenSSH has no ControlMaster/-f support, so connection
// reuse (mux) is Linux/macOS only. Windows falls back to per-command connects.
var isWindows = runtime.GOOS == "windows"

type Ctrl struct {
	runner osutil.Runner
	emit   forward.EmitFunc
	active map[string]bool   // hosts marked connected (Windows per-command mode)
	users  map[string]string // host → 最近一次使用的 user（用于把 user 纳入 socket key）
	mu     sync.Mutex
}

func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{runner: r, emit: emit, active: map[string]bool{}, users: map[string]string{}}
}

// controlPathFor 委托给 sshconn（socket 路径的唯一来源）；user 纳入 key，
// 否则同 host 异 user 会共用 master，用错身份读写远端文件。
func (c *Ctrl) controlPathFor(host, user string) string {
	return sshconn.ControlPath(host, user)
}

func (c *Ctrl) rememberUser(host, user string) {
	c.mu.Lock()
	if c.users == nil {
		c.users = map[string]string{}
	}
	c.users[host] = user
	c.mu.Unlock()
}

// userFor 返回该 host 最近一次使用的 user；无记录时返回空串，即
// ControlPath(host, "")，与旧版 cm-<host>.sock 完全一致。
func (c *Ctrl) userFor(host string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.users[host]
}

func (c *Ctrl) run(host, user string, batch []byte) (osutil.Outcome, error) {
	c.rememberUser(host, user)
	dir, err := os.MkdirTemp("", "sshore-sftp")
	if err != nil {
		return osutil.Outcome{}, err
	}
	defer os.RemoveAll(dir)
	batchPath, err := c.writeBatch(dir, batch)
	if err != nil {
		return osutil.Outcome{}, err
	}
	cp := c.controlPathFor(host, user)
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-b", batchPath,
	}
	if !isWindows {
		// Linux/macOS reuse one SSH connection via ControlMaster.
		args = append(args,
			"-o", "ControlMaster=no",
			"-o", "ControlPersist=30",
			"-o", "ControlPath="+cp,
		)
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host)
	return c.runner("sftp", args...)
}

// CloseAll closes any lingering ControlMaster connections and removes sockets.
// socket 名形如 cm-<host>+<user>.sock，从文件名反解不出 host，因此改为遍历
// 内存中的 host→user 映射；随后仍清掉整个 control 目录。
func (c *Ctrl) CloseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, user := range c.users {
		_ = c.disconnectLocked(host, user)
	}
	_ = os.RemoveAll(sshconn.ControlDir())
}

// logEvent 记录一条 SFTP 操作日志(SourceType=sftp, SourceID=host),
// 供前端日志面板展示/按主机过滤。emit 为 nil(测试/未接线)时静默。
func (c *Ctrl) logEvent(host, level, msg string) {
	if c.emit != nil {
		c.emit(forward.Event{
			SourceType: "sftp",
			SourceID:   host,
			TS:         time.Now().Format(time.RFC3339),
			Level:      level,
			Message:    msg,
		})
	}
}

// Connect establishes a ControlMaster SSH connection for host so subsequent
// sftp ops reuse it. This is the explicit "connect" action.
// On Windows (no ControlMaster/-f) it instead verifies connectivity by
// running a throwaway sftp command, and marks the host as connected.
func (c *Ctrl) Connect(host, user string) error {
	c.logEvent(host, "info", "sftp connect "+host)
	if isWindows {
		// Per-command mode: prove connectivity with a quick `sftp ls .`.
		batch, err := c.buildBatch("ls", ".", "")
		if err != nil {
			return err
		}
		out, err := c.run(host, user, batch)
		if err != nil {
			return fmt.Errorf("sftp connect %s: %w (%s)", host, err, commandErr(out))
		}
		if out.ExitCode != 0 {
			return fmt.Errorf("sftp connect %s failed: %s", host, commandErr(out))
		}
		c.mu.Lock()
		c.active[host] = true
		c.mu.Unlock()
		c.logEvent(host, "info", "sftp connect "+host+" ok")
		return nil
	}

	c.rememberUser(host, user)
	cp := c.controlPathFor(host, user)
	args := []string{"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=10", "-o", "ControlMaster=yes", "-o", "ControlPersist=30", "-o", "ControlPath=" + cp, "-N", "-f"}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host)
	out, err := c.runner("ssh", args...)
	if err != nil {
		return fmt.Errorf("sftp connect %s: %w (%s)", host, err, out.Stderr)
	}
	// Confirm the master socket exists (the -f ssh may fork and return before binding).
	for i := 0; i < 20; i++ {
		if _, statErr := os.Stat(cp); statErr == nil {
			c.logEvent(host, "info", "sftp connect "+host+" ok")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("sftp connect %s: ControlPath not created", host)
}

// Disconnect closes the ControlMaster connection for host.
// On Windows it just clears the connected mark (per-command mode).
func (c *Ctrl) Disconnect(host string) error {
	c.logEvent(host, "info", "sftp disconnect "+host)
	c.mu.Lock()
	defer c.mu.Unlock()
	if isWindows {
		delete(c.active, host)
		return nil
	}
	return c.disconnectLocked(host, c.users[host])
}

// disconnectLocked 需在持有 c.mu 时调用；user 由调用方传入（map 读在锁内完成），
// 不能在此调用 userFor，否则会重复加锁。
func (c *Ctrl) disconnectLocked(host, user string) error {
	cp := c.controlPathFor(host, user)
	out, err := c.runner("ssh", "-O", "exit", "-o", "ControlPath="+cp, "-o", "ConnectTimeout=10", host)
	if err != nil {
		return fmt.Errorf("sftp disconnect %s: %w (%s)", host, err, out.Stderr)
	}
	_ = os.Remove(cp)
	return nil
}

// Connected reports whether a ControlMaster connection exists for host.
// On Windows it reports the in-memory connected mark instead.
func (c *Ctrl) Connected(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if isWindows {
		return c.active[host]
	}
	cp := c.controlPathFor(host, c.users[host])
	_, err := os.Stat(cp)
	return err == nil
}

// quoteArg validates a path for batch use and wraps it in double quotes so the
// sftp batch parser treats it as a single argument. Control characters (e.g.
// \n) are rejected outright: a newline would split the batch into two commands,
// and sftp batch lines starting with `!` execute local shell commands.
func quoteArg(p string) (string, error) {
	for _, r := range p {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("路径包含非法控制字符: %q", p)
		}
	}
	p = strings.ReplaceAll(p, "\\", "\\\\")
	p = strings.ReplaceAll(p, "\"", "\\\"")
	return "\"" + p + "\"", nil
}

func (c *Ctrl) buildBatch(op, remote, local string) ([]byte, error) {
	switch op {
	case "ls":
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		// -a includes hidden entries (dotfiles); ParseLsLf filters out "."/"..".
		return []byte(fmt.Sprintf("ls -la %s\n", r)), nil
	case "get":
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		l, err := quoteArg(local)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("get %s %s\n", r, l)), nil
	case "getr":
		// Recursive directory download; sftp creates the local target dir.
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		l, err := quoteArg(local)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("get -r %s %s\n", r, l)), nil
	case "put":
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		l, err := quoteArg(local)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("put %s %s\n", l, r)), nil
	case "rm":
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("rm %s\n", r)), nil
	case "mkdir":
		r, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("mkdir %s\n", r)), nil
	case "rename":
		o, err := quoteArg(remote)
		if err != nil {
			return nil, err
		}
		n, err := quoteArg(local)
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("rename %s %s\n", o, n)), nil
	default:
		return nil, nil
	}
}

func (c *Ctrl) writeBatch(dir string, content []byte) (string, error) {
	f, err := os.CreateTemp(dir, "sshore-sftp-*.bat")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := f.Write(content); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func (c *Ctrl) List(host, user, path string) ([]Item, error) {
	c.logEvent(host, "info", "sftp ls "+path)
	batch, err := c.buildBatch("ls", path, "")
	if err != nil {
		c.logEvent(host, "error", "sftp ls failed: "+err.Error())
		return nil, err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp ls failed: "+commandErr(out))
		return nil, fmt.Errorf("sftp ls %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp ls failed: "+commandErr(out))
		return nil, fmt.Errorf("sftp ls failed: %s", commandErr(out))
	}
	items, err := ParseLsLf(out.Stdout)
	if err != nil {
		c.logEvent(host, "error", "sftp ls parse failed: "+err.Error())
		return nil, err
	}
	c.logEvent(host, "info", fmt.Sprintf("sftp ls done (%d items)", len(items)))
	return items, nil
}

// ListMany 在一个 sftp 批处理里列出多个远端目录，返回 path -> items。
// 返回的 map 中缺失某个 key 表示"该目录未知"，绝不表示"空目录"——调用方必须凭
// 缺失的 key 判断完整性，否则会把"列不出来"当成"目录为空"而误删本地文件。
func (c *Ctrl) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	if len(paths) == 0 {
		return map[string][]Item{}, nil
	}
	c.logEvent(host, "info", fmt.Sprintf("sftp ls many (%d dirs)", len(paths)))
	var sb strings.Builder
	for _, p := range paths {
		q, err := quoteArg(p)
		if err != nil {
			c.logEvent(host, "error", "sftp ls many failed: "+err.Error())
			return nil, fmt.Errorf("ListMany: %w", err)
		}
		// 前缀 '-' 抑制逐命令中止：man sftp 明确 ls 失败会中止整批，
		// 任一个子目录不可读就会让后面所有目录永远列不出来。
		sb.WriteString("-ls -la " + q + "\n")
	}
	out, err := c.run(host, user, []byte(sb.String()))
	if err != nil {
		c.logEvent(host, "error", "sftp ls many failed: "+commandErr(out))
		return nil, fmt.Errorf("sftp ListMany %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp ls many failed: "+commandErr(out))
		return nil, fmt.Errorf("sftp ListMany failed: %s", commandErr(out))
	}
	res := parseListMany(out.Stdout, paths)
	// stderr 是客户端错误行的真实来源(实测 OpenSSH sftp)。stdout 里失败目录只剩回显行,
	// 空块不变量已把它判为未知;这里再按请求路径反查 stderr:既兜底,
	// 又把"为什么列不出来"的远端原文写进日志。
	for _, p := range paths {
		if msg := stderrListFailure(out.Stderr, p); msg != "" {
			delete(res, p)
			c.logEvent(host, "error", "sftp ls "+p+" failed: "+msg)
		}
	}
	c.logEvent(host, "info", fmt.Sprintf("sftp ls many done (%d/%d dirs)", len(res), len(paths)))
	return res, nil
}

// Home returns the remote user's home directory by running `pwd`. The output
// is the sftp prompt line followed by the absolute home path, which we extract.
func (c *Ctrl) Home(host, user string) (string, error) {
	c.logEvent(host, "info", "sftp pwd")
	out, err := c.run(host, user, []byte("pwd\n"))
	if err != nil {
		c.logEvent(host, "error", "sftp pwd failed: "+commandErr(out))
		return "", fmt.Errorf("sftp pwd %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp pwd failed: "+commandErr(out))
		return "", fmt.Errorf("sftp pwd failed: %s", commandErr(out))
	}
	// pwd output: "sftp> pwd\nRemote working directory: <abs>\n" (or bare "<abs>")
	for _, line := range strings.Split(out.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Remote working directory: ") {
			p := strings.TrimSpace(strings.TrimPrefix(line, "Remote working directory: "))
			if p != "" {
				return p, nil
			}
		}
		if strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "sftp>") {
			return line, nil
		}
	}
	return "", fmt.Errorf("sftp pwd: no absolute path in output %q", out.Stdout)
}

// commandErr returns stderr (or stdout) trimmed, preferring stderr, so the
// original sftp/ssh message (e.g. "Host key verification failed", "Permission
// denied") is surfaced to the user instead of a bare exit code.
func commandErr(out osutil.Outcome) string {
	s := out.Stderr
	if s == "" {
		s = out.Stdout
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Sprintf("exit %d", out.ExitCode)
	}
	return s
}

// humanSize 将字节数格式化为可读大小(B/KB/MB/GB,保留 1 位小数)。
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/1024)
}

// remoteSize 通过一次 `sftp ls -la <file>` 查询远端单文件大小;
// 路径不存在/是目录/查询失败时返回错误(日志降级为不带大小,不阻塞传输)。
func (c *Ctrl) remoteSize(host, user, path string) (int64, error) {
	batch, err := c.buildBatch("ls", path, "")
	if err != nil {
		return -1, err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return -1, err
	}
	if out.ExitCode != 0 {
		return -1, fmt.Errorf("ls exit %d", out.ExitCode)
	}
	items, err := ParseLsLf(out.Stdout)
	if err != nil || len(items) != 1 || items[0].IsDir {
		return -1, fmt.Errorf("not a regular file")
	}
	return items[0].Size, nil
}

func (c *Ctrl) Get(host, user, remote, local string) error {
	msg := "sftp get " + remote + " → " + local
	if size, err := c.remoteSize(host, user, remote); err == nil {
		msg += fmt.Sprintf(" (%s)", humanSize(size))
	}
	c.logEvent(host, "info", msg)
	batch, err := c.buildBatch("get", remote, local)
	if err != nil {
		c.logEvent(host, "error", "sftp get failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp get failed: "+commandErr(out))
		return fmt.Errorf("sftp get %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp get failed: "+commandErr(out))
		return fmt.Errorf("sftp get failed: %s", commandErr(out))
	}
	done := "sftp get done"
	if st, statErr := os.Stat(local); statErr == nil {
		done += fmt.Sprintf(" (%s)", humanSize(st.Size()))
	}
	c.logEvent(host, "info", done)
	return nil
}

// GetRecursive downloads a remote directory tree with `sftp get -r`.
func (c *Ctrl) GetRecursive(host, user, remote, local string) error {
	c.logEvent(host, "info", "sftp get -r "+remote+" → "+local+" (directory)")
	batch, err := c.buildBatch("getr", remote, local)
	if err != nil {
		c.logEvent(host, "error", "sftp get -r failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp get -r failed: "+commandErr(out))
		return fmt.Errorf("sftp get -r %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp get -r failed: "+commandErr(out))
		return fmt.Errorf("sftp get -r failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp get -r done")
	return nil
}

func (c *Ctrl) Put(host, user, local, remote string) error {
	msg := "sftp put " + local + " → " + remote
	size := int64(-1)
	if st, err := os.Stat(local); err == nil {
		size = st.Size()
		msg += fmt.Sprintf(" (%s)", humanSize(size))
	}
	c.logEvent(host, "info", msg)
	batch, err := c.buildBatch("put", remote, local)
	if err != nil {
		c.logEvent(host, "error", "sftp put failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp put failed: "+commandErr(out))
		return fmt.Errorf("sftp put %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp put failed: "+commandErr(out))
		return fmt.Errorf("sftp put failed: %s", commandErr(out))
	}
	done := "sftp put done"
	if size >= 0 {
		done += fmt.Sprintf(" (%s)", humanSize(size))
	}
	c.logEvent(host, "info", done)
	return nil
}

func (c *Ctrl) Remove(host, user, path string) error {
	c.logEvent(host, "info", "sftp rm "+path)
	batch, err := c.buildBatch("rm", path, "")
	if err != nil {
		c.logEvent(host, "error", "sftp rm failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp rm failed: "+commandErr(out))
		return fmt.Errorf("sftp rm %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp rm failed: "+commandErr(out))
		return fmt.Errorf("sftp rm failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp rm done")
	return nil
}

func (c *Ctrl) Mkdir(host, user, path string) error {
	c.logEvent(host, "info", "sftp mkdir "+path)
	batch, err := c.buildBatch("mkdir", path, "")
	if err != nil {
		c.logEvent(host, "error", "sftp mkdir failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp mkdir failed: "+commandErr(out))
		return fmt.Errorf("sftp mkdir %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp mkdir failed: "+commandErr(out))
		return fmt.Errorf("sftp mkdir failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp mkdir done")
	return nil
}

// Rename renames/moves a remote file or directory.
func (c *Ctrl) Rename(host, user, oldPath, newPath string) error {
	c.logEvent(host, "info", "sftp rename "+oldPath+" → "+newPath)
	batch, err := c.buildBatch("rename", oldPath, newPath)
	if err != nil {
		c.logEvent(host, "error", "sftp rename failed: "+err.Error())
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		c.logEvent(host, "error", "sftp rename failed: "+commandErr(out))
		return fmt.Errorf("sftp rename %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		c.logEvent(host, "error", "sftp rename failed: "+commandErr(out))
		return fmt.Errorf("sftp rename failed: %s", commandErr(out))
	}
	c.logEvent(host, "info", "sftp rename done")
	return nil
}
