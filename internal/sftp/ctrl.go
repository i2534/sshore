package sftp

import (
	"fmt"
	"os"
	"strings"

	"sshkit/internal/forward"
	"sshkit/internal/osutil"
)

type Ctrl struct {
	runner osutil.Runner
	emit   forward.EmitFunc
}

func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{runner: r, emit: emit}
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
	args := []string{"-o", "BatchMode=yes", "-b", batchPath}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host)
	return c.runner("sftp", args...)
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
