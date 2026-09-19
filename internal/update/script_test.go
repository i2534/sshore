package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestScriptName(t *testing.T) {
	if ScriptName("windows") != "sshore-update.cmd" {
		t.Fatalf("windows 脚本名错误: %s", ScriptName("windows"))
	}
	if ScriptName("linux") != "sshore-update.sh" {
		t.Fatalf("linux 脚本名错误: %s", ScriptName("linux"))
	}
}

func TestScriptBytesAreSafeAndLFOnly(t *testing.T) {
	sh, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sh), "\r\n") {
		t.Fatal("update.sh 不能含 CRLF（spec §8.1）")
	}
	for _, banned := range []string{"powershell", "curl", "wget", "certutil", "eval"} {
		if strings.Contains(string(sh), banned) {
			t.Errorf("update.sh 不得出现 %q", banned)
		}
	}
	cmd, err := ScriptBytes("windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"powershell", "curl", "wget", "certutil"} {
		if strings.Contains(strings.ToLower(string(cmd)), banned) {
			t.Errorf("update.cmd 不得出现 %q", banned)
		}
	}
	for _, want := range []string{"SSHORE_PENDING", "RESULT=ok", "enabledelayedexpansion"} {
		if !strings.Contains(string(cmd), want) {
			t.Errorf("update.cmd 缺少 %q", want)
		}
	}
}

func TestScriptArgsAndEnv(t *testing.T) {
	p := PlanFor("linux", "/opt/sshore/sshore", "v0.6.0", "v0.7.0", 42, 30*time.Second)
	args := ScriptArgs(p, 4242)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--pid 4242", "--size 42", "--wait 30", p.Pending, p.Backup, p.LogPath} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv 缺少 %q: %v", want, args)
		}
	}
	env := ScriptEnv(p, 4242)
	want := map[string]string{
		"SSHORE_PID":     "4242",
		"SSHORE_TARGET":  p.Target,
		"SSHORE_PENDING": p.Pending,
		"SSHORE_BACKUP":  p.Backup,
		"SSHORE_SIZE":    "42",
		"SSHORE_LOG":     p.LogPath,
		"SSHORE_WAIT":    "30",
	}
	got := map[string]string{}
	for _, kv := range env {
		i := strings.Index(kv, "=")
		got[kv[:i]] = kv[i+1:]
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// scriptFixture 造出「旧二进制 + 待安装文件」的最小临时目录，供真跑 update.sh 做参数校验。
func scriptFixture(t *testing.T) Plan {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(p.Pending, []byte("PENDING\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p.Pending)
	if err != nil {
		t.Fatal(err)
	}
	p.Size = st.Size()
	return p
}

// runScriptArgs 写出嵌入的 update.sh 并用给定参数真跑，返回日志内容与错误。
func runScriptArgs(t *testing.T, p Plan, args ...string) (string, error) {
	t.Helper()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(p.ExeDir, ScriptName("linux"))
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Dir = p.ExeDir
	runErr := cmd.Run()
	log, _ := os.ReadFile(p.LogPath)
	return string(log), runErr
}

// deadPID 起一个立刻退出的子进程并 reap，返回一个确定已死的 PID。
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	return cmd.Process.Pid
}

// TestScriptArgsValidation 覆盖 spec §8.2/§8.3 的三类参数失败：① --target 不存在、
// ② --backup 父目录不存在、③ --pid 非数字。三者都必须写日志末行 RESULT=fail:args 并退 2。
func TestScriptArgsValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("update.sh 只能在类 Unix 上真跑")
	}
	cases := []struct {
		name    string
		wantErr string
		args    func(t *testing.T, p Plan) []string
	}{
		{
			name:    "target 不存在",
			wantErr: "ERR=target 不存在",
			args: func(t *testing.T, p Plan) []string {
				p.Target = filepath.Join(p.ExeDir, "not-there")
				return ScriptArgs(p, deadPID(t))
			},
		},
		{
			name:    "backup 父目录不存在",
			wantErr: "ERR=backup 父目录不存在",
			args: func(t *testing.T, p Plan) []string {
				p.Backup = filepath.Join(t.TempDir(), "missing-dir", "sshore.v0.6.0")
				return ScriptArgs(p, deadPID(t))
			},
		},
		{
			name:    "pid 非数字",
			wantErr: "ERR=pid 非法",
			args: func(t *testing.T, p Plan) []string {
				return []string{
					"--pid", "abc",
					"--target", p.Target,
					"--pending", p.Pending,
					"--backup", p.Backup,
					"--size", strconv.FormatInt(p.Size, 10),
					"--log", p.LogPath,
					"--wait", "30",
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := scriptFixture(t)
			log, err := runScriptArgs(t, p, tc.args(t, p)...)
			if err == nil {
				t.Fatalf("参数非法必须失败, log=%s", log)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("参数类失败必须退 2，实际 %v（log=%s）", err, log)
			}
			if !strings.Contains(log, tc.wantErr) {
				t.Fatalf("日志应含 %q: %s", tc.wantErr, log)
			}
			if !strings.HasSuffix(strings.TrimSpace(log), "RESULT=fail:args") {
				t.Fatalf("日志末行必须是 RESULT=fail:args: %q", log)
			}
		})
	}
}
