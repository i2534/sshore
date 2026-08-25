package sftp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sshkit/internal/forward"
	"sshkit/internal/osutil"
)

type Ctrl struct {
	runner     osutil.Runner
	emit       forward.EmitFunc
	controlDir string
	mu         sync.Mutex
}

func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{runner: r, emit: emit, controlDir: filepath.Join(os.TempDir(), "sshkit-sftp-ctrl")}
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
	dir, err := os.MkdirTemp("", "sshkit-sftp")
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
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=30",
		"-o", "ControlPath=" + cp,
		"-b", batchPath,
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
	_ = os.RemoveAll(c.controlDir)
}

func (c *Ctrl) buildBatch(op, remote, local string) []byte {
	switch op {
	case "ls":
		return []byte(fmt.Sprintf("ls -l %s\n", remote))
	case "get":
		return []byte(fmt.Sprintf("get %s %s\n", remote, local))
	case "put":
		return []byte(fmt.Sprintf("put %s %s\n", local, remote))
	case "rm":
		return []byte(fmt.Sprintf("rm %s\n", remote))
	case "mkdir":
		return []byte(fmt.Sprintf("mkdir %s\n", remote))
	default:
		return nil
	}
}

func (c *Ctrl) writeBatch(dir string, content []byte) (string, error) {
	f, err := os.CreateTemp(dir, "sshkit-sftp-*.bat")
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
	out, err := c.run(host, user, c.buildBatch("ls", path, ""))
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
	out, err := c.run(host, user, c.buildBatch("get", remote, local))
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp get failed: %s", out.Stderr)
	}
	return nil
}

func (c *Ctrl) Put(host, user, local, remote string) error {
	out, err := c.run(host, user, c.buildBatch("put", remote, local))
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp put failed: %s", out.Stderr)
	}
	return nil
}

func (c *Ctrl) Remove(host, user, path string) error {
	out, err := c.run(host, user, c.buildBatch("rm", path, ""))
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp rm failed: %s", out.Stderr)
	}
	return nil
}

func (c *Ctrl) Mkdir(host, user, path string) error {
	out, err := c.run(host, user, c.buildBatch("mkdir", path, ""))
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp mkdir failed: %s", out.Stderr)
	}
	return nil
}

// Rename renames/moves a remote file or directory.
func (c *Ctrl) Rename(host, user, oldPath, newPath string) error {
	batch := []byte(fmt.Sprintf("rename %s %s\n", oldPath, newPath))
	out, err := c.run(host, user, batch)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sftp rename failed: %s", out.Stderr)
	}
	return nil
}
