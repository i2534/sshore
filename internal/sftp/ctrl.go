package sftp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// isWindows: Windows OpenSSH has no ControlMaster/-f support, so connection
// reuse (mux) is Linux/macOS only. Windows falls back to per-command connects.
var isWindows = runtime.GOOS == "windows"

type Ctrl struct {
	runner     osutil.Runner
	emit       forward.EmitFunc
	controlDir string
	active     map[string]bool // hosts marked connected (Windows per-command mode)
	mu         sync.Mutex
}

func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{runner: r, emit: emit, controlDir: filepath.Join(os.TempDir(), "sshore-sftp-ctrl"), active: map[string]bool{}}
}

// controlPathFor returns a per-host ControlMaster socket path (URL-encoded so
// arbitrary host strings map to a safe filename). Reusing this socket lets
// successive sftp commands share one SSH connection (ControlMaster=auto).
func (c *Ctrl) controlPathFor(host string) string {
	_ = os.MkdirAll(c.controlDir, 0700)
	name := url.PathEscape(host)
	return filepath.Join(c.controlDir, "cm-"+name+".sock")
}

func (c *Ctrl) run(host, user string, batch []byte) (osutil.Outcome, error) {
	dir, err := os.MkdirTemp("", "sshore-sftp")
	if err != nil {
		return osutil.Outcome{}, err
	}
	defer os.RemoveAll(dir)
	batchPath, err := c.writeBatch(dir, batch)
	if err != nil {
		return osutil.Outcome{}, err
	}
	cp := c.controlPathFor(host)
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
func (c *Ctrl) CloseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.controlDir == "" {
		return
	}
	entries, _ := os.ReadDir(c.controlDir)
	for _, e := range entries {
		name := strings.TrimPrefix(e.Name(), "cm-")
		name = strings.TrimSuffix(name, ".sock")
		if unescaped, err := url.PathUnescape(name); err == nil {
			_ = c.disconnectLocked(unescaped)
		}
	}
	_ = os.RemoveAll(c.controlDir)
}

// Connect establishes a ControlMaster SSH connection for host so subsequent
// sftp ops reuse it. This is the explicit "connect" action.
// On Windows (no ControlMaster/-f) it instead verifies connectivity by
// running a throwaway sftp command, and marks the host as connected.
func (c *Ctrl) Connect(host, user string) error {
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
		return nil
	}

	cp := c.controlPathFor(host)
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
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("sftp connect %s: ControlPath not created", host)
}

// Disconnect closes the ControlMaster connection for host.
// On Windows it just clears the connected mark (per-command mode).
func (c *Ctrl) Disconnect(host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if isWindows {
		delete(c.active, host)
		return nil
	}
	return c.disconnectLocked(host)
}

func (c *Ctrl) disconnectLocked(host string) error {
	cp := c.controlPathFor(host)
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
	cp := c.controlPathFor(host)
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
	batch, err := c.buildBatch("ls", path, "")
	if err != nil {
		return nil, err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return nil, fmt.Errorf("sftp ls %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return nil, fmt.Errorf("sftp ls failed: %s", commandErr(out))
	}
	return ParseLsLf(out.Stdout)
}

// Home returns the remote user's home directory by running `pwd`. The output
// is the sftp prompt line followed by the absolute home path, which we extract.
func (c *Ctrl) Home(host, user string) (string, error) {
	out, err := c.run(host, user, []byte("pwd\n"))
	if err != nil {
		return "", fmt.Errorf("sftp pwd %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
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

func (c *Ctrl) Get(host, user, remote, local string) error {
	batch, err := c.buildBatch("get", remote, local)
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp get %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp get failed: %s", commandErr(out))
	}
	return nil
}

// GetRecursive downloads a remote directory tree with `sftp get -r`.
func (c *Ctrl) GetRecursive(host, user, remote, local string) error {
	batch, err := c.buildBatch("getr", remote, local)
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp get -r %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp get -r failed: %s", commandErr(out))
	}
	return nil
}

func (c *Ctrl) Put(host, user, local, remote string) error {
	batch, err := c.buildBatch("put", remote, local)
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp put %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp put failed: %s", commandErr(out))
	}
	return nil
}

func (c *Ctrl) Remove(host, user, path string) error {
	batch, err := c.buildBatch("rm", path, "")
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp rm %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp rm failed: %s", commandErr(out))
	}
	return nil
}

func (c *Ctrl) Mkdir(host, user, path string) error {
	batch, err := c.buildBatch("mkdir", path, "")
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp mkdir %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp mkdir failed: %s", commandErr(out))
	}
	return nil
}

// Rename renames/moves a remote file or directory.
func (c *Ctrl) Rename(host, user, oldPath, newPath string) error {
	batch, err := c.buildBatch("rename", oldPath, newPath)
	if err != nil {
		return err
	}
	out, err := c.run(host, user, batch)
	if err != nil {
		return fmt.Errorf("sftp rename %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp rename failed: %s", commandErr(out))
	}
	return nil
}
