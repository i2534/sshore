package update

import (
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
