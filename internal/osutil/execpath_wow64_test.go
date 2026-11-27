package osutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// F3 回归：windows/386 进程里 WOW64 把 ProgramFiles 改写为
// C:\Program Files (x86)；64 位 Program Files 目录必须读 ProgramW6432。
// 注入两个环境变量后，候选目录必须含 64 位 OpenSSH，且 (x86) 路径不出现
// （实现语义：ProgramW6432 优先，缺失才回退 ProgramFiles，二者只取其一）。
func TestWindowsOpenSSHDirsPrefersProgramW6432(t *testing.T) {
	installSeams(t, "windows", map[string]string{
		"SystemRoot":   testSystemRoot,
		"ProgramW6432": `C:\Program Files`,
		"ProgramFiles": `C:\Program Files (x86)`,
	}, lookPathMiss(), statOnlyFiles())
	dirs := windowsOpenSSHDirs()
	want := filepath.Join(`C:\Program Files`, "OpenSSH")
	found := false
	for _, d := range dirs {
		if d == want {
			found = true
		}
		if strings.Contains(d, "(x86)") {
			t.Fatalf("WOW64 改写后的 (x86) 目录不得作为候选: %v", dirs)
		}
	}
	if !found {
		t.Fatalf("dirs 必须含 64 位 Program Files 的 OpenSSH %q, got %v", want, dirs)
	}
}

// F3 端到端：只有 64 位 Program Files\OpenSSH\ssh.exe 存在时，
// 32 位视角（ProgramFiles 被改写为 x86）也必须解析到它，而不是回退裸名。
func TestResolveSSHWindowsProgramW6432BeatsX86(t *testing.T) {
	pf64 := filepath.Join(`C:\Program Files`, "OpenSSH", "ssh.exe")
	installSeams(t, "windows", map[string]string{
		"SystemRoot":   testSystemRoot,
		"ProgramW6432": `C:\Program Files`,
		"ProgramFiles": `C:\Program Files (x86)`,
	}, lookPathMiss(), statOnlyFiles(pf64))
	if got := SSHExecutable(); got != pf64 {
		t.Fatalf("SSHExecutable() = %q, want %q（ProgramW6432 必须压过 WOW64 改写的 ProgramFiles）", got, pf64)
	}
}

// F3 守卫：只有 ProgramFiles（无 ProgramW6432，如原生 32 位系统或 amd64 进程）
// 时行为不变——既有 TestResolveSSHWindowsProgramFilesFallback 已覆盖命中路径，
// 这里补一条 dirs 级断言钉住回退语义。
func TestWindowsOpenSSHDirsFallsBackToProgramFiles(t *testing.T) {
	installSeams(t, "windows", map[string]string{
		"SystemRoot":   testSystemRoot,
		"ProgramFiles": `C:\Program Files`,
	}, lookPathMiss(), statOnlyFiles())
	dirs := windowsOpenSSHDirs()
	want := filepath.Join(`C:\Program Files`, "OpenSSH")
	if dirs[len(dirs)-1] != want {
		t.Fatalf("无 ProgramW6432 时必须回退 ProgramFiles, got %v want 末位 %q", dirs, want)
	}
}
