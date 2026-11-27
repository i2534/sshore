//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// killFakeBinary 与 newFixture 的收尾相同：杀掉假二进制写下的 fake.pid 进程。
func killFakeBinary(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(dir, "fake.pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	})
}

// F8 回归：tag 不带 v 前缀（cleanRe 允许 0.6.0 形态）时备份名是 sshore.0.6.0，
// 旧清理 glob 只有 sshore.v* 与 sshore.dev-*，匹配不到 → 旧备份无限累积。
// 场景：当前 0.7.0 → 目标 0.8.0（全不带 v），目录里预置更早的无 v 备份
// sshore.0.6.0 与带 v 备份 sshore.v0.5.0。成功跑完脚本后：
// 两份旧备份都被清、本次备份 sshore.0.7.0 保留、sidecar 保留、锁文件不动。
func TestScriptRunCleanupNoVPrefixBackup(t *testing.T) {
	requirePOSIXSh(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := PlanFor("linux", exe, "0.7.0", "0.8.0", 0, DefaultWait)
	if err := os.WriteFile(p.Pending, []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	// 更早的无 v 前缀备份（本 bug 的判别点：旧 glob 漏掉它）。
	if err := os.WriteFile(filepath.Join(dir, "sshore.0.6.0"), []byte("OLDNOV\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 带 v 的旧备份（既有行为对照）。
	if err := os.WriteFile(filepath.Join(dir, "sshore.v0.5.0"), []byte("OLDER\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// sidecar：清理绝不得碰它（case *.sha256 排除）。
	if err := os.WriteFile(p.Sidecar, []byte("x  "+filepath.Base(p.Pending)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	killFakeBinary(t, dir)

	if _, err := runScript(t, p, exitedChild(t)); err != nil {
		t.Fatalf("脚本应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sshore.0.6.0")); !os.IsNotExist(err) {
		t.Fatal("无 v 前缀的旧备份 sshore.0.6.0 必须被清理（只留最近一份）")
	}
	if _, err := os.Stat(filepath.Join(dir, "sshore.v0.5.0")); !os.IsNotExist(err) {
		t.Fatal("带 v 的旧备份 sshore.v0.5.0 必须被清理")
	}
	got, err := os.ReadFile(p.Backup)
	if err != nil || string(got) != "OLD\n" {
		t.Fatalf("本次备份 sshore.0.7.0 必须保留且为旧二进制: %q %v", got, err)
	}
	if _, err := os.Stat(p.Sidecar); err != nil {
		t.Fatalf("sidecar 必须保留: %v", err)
	}
	if got, _ := os.ReadFile(p.Target); !strings.HasPrefix(string(got), "#!/bin/sh") {
		t.Fatalf("正式二进制必须是新版: %q", got)
	}
	assertLockKept(t, p)
}

// F8 守卫：新 glob 会匹配到无 v 前缀的 pending（sshore.0.7.0）与 sidecar，
// 第 4 步清理执行后它们必须原封不动（PB 排除 + case *.sha256 排除）。
// 用「backup 父目录存在但只读」让第 5 步失败，确保第 4 步一定跑过。
func TestScriptRunCleanupNoVPrefixKeepsPendingAndSidecar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 有 CAP_DAC_OVERRIDE，0500 构造不出只读目录")
	}
	requirePOSIXSh(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := PlanFor("linux", exe, "0.6.0", "0.7.0", 0, DefaultWait)
	if err := os.WriteFile(p.Pending, []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	if err := os.WriteFile(filepath.Join(dir, "sshore.0.5.0"), []byte("OLDNOV\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Sidecar, []byte("x  "+filepath.Base(p.Pending)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roDir := filepath.Join(dir, "ro-backup")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
	p.Backup = filepath.Join(roDir, "sshore.0.6.0")
	killFakeBinary(t, dir)

	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("第 5 步备份失败必须报错, log=%s", log)
	}
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("备份失败必须退 3，实际 %d（log=%s）", code, log)
	}
	assertLastResult(t, log, "RESULT=fail:5")
	// 第 4 步跑过的证据：无 v 旧备份已被清理。
	if _, err := os.Stat(filepath.Join(dir, "sshore.0.5.0")); !os.IsNotExist(err) {
		t.Fatalf("第 4 步应清理无 v 旧备份 sshore.0.5.0: %v", err)
	}
	// 新 glob 匹配得到 pending，但 PB 排除必须保住它。
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("第 4 步不得删无 v 前缀的 pending: %v", err)
	}
	if _, err := os.Stat(p.Sidecar); err != nil {
		t.Fatalf("第 4 步不得删 sidecar: %v", err)
	}
	if got, _ := os.ReadFile(p.Target); string(got) != "OLD\n" {
		t.Fatalf("失败时正式二进制必须保持旧内容: %q", got)
	}
	assertLockKept(t, p)
}
