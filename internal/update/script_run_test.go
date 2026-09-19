//go:build !windows

package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runScript 在临时目录里跑生成的 update.sh，返回日志内容与错误。
func runScript(t *testing.T, plan Plan, pid int) (string, error) {
	t.Helper()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(plan.ExeDir, "sshore-update.sh")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, ScriptArgs(plan, pid)...)
	cmd.Dir = plan.ExeDir
	err = cmd.Run()
	log, _ := os.ReadFile(plan.LogPath)
	return string(log), err
}

// runScriptArgv 与 runScript 相同，只是 argv 由调用方给出（--pid 非数字等场景无法用 ScriptArgs 构造）。
func runScriptArgv(t *testing.T, plan Plan, args ...string) (string, error) {
	t.Helper()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(plan.ExeDir, "sshore-update.sh")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Dir = plan.ExeDir
	runErr := cmd.Run()
	log, _ := os.ReadFile(plan.LogPath)
	return string(log), runErr
}

// newFixture 造出旧二进制 + 待安装文件 + 一份更早的备份。
// fakeBinary 是「可执行且能存活 >3s」的假二进制：脚本第 8 步会做 3 秒存活探测，
// 用不可执行的字面量会让脚本走回滚分支（两阶段评审实测）。它把 PID 写进 fake.pid，
// 由 t.Cleanup 杀掉，避免测试留下孤儿进程。
const fakeBinary = "#!/bin/sh\necho $$ > \"$(dirname \"$0\")/fake.pid\"\nsleep 30\n"

// lockName 是 Linux 侧的升级锁文件名（spec §7.6/§8.4）：加锁是主程序的职责，
// 脚本本身不得创建、改动或删除它（Task 12 裁定 3）。
const lockName = ".sshore-update.lock"

// requirePOSIXSh 在没有系统 sh 时跳过而不是失败：脚本本身与夹具都依赖 POSIX sh。
func requirePOSIXSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("没有 /bin/sh，跳过真实脚本执行: %v", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("PATH 中没有 sh，跳过真实脚本执行: %v", err)
	}
	if out, err := exec.Command(sh, "-c", ":").CombinedOutput(); err != nil {
		t.Skipf("sh 不是可用的 POSIX shell（%s）: %v %s", sh, err, out)
	}
}

func newFixture(t *testing.T) Plan {
	t.Helper()
	requirePOSIXSh(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(p.Pending, []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sshore.v0.5.0"), []byte("OLDER\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 锁文件（Task 12 裁定 3）：夹具放进目标目录，任何路径结束后都必须原封不动。
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	return p
}

// assertLockKept 断言锁文件仍在且内容未被脚本改动。
func assertLockKept(t *testing.T, p Plan) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(p.ExeDir, lockName))
	if err != nil {
		t.Fatalf("脚本不得删除 %s: %v", lockName, err)
	}
	if string(got) != "lock\n" {
		t.Fatalf("%s 被脚本改动: %q", lockName, got)
	}
}

// assertLastResult 断言日志末行就是结果协议行（spec §8.6）。
func assertLastResult(t *testing.T, log, want string) {
	t.Helper()
	if got := strings.TrimSpace(log); !strings.HasSuffix(got, want) {
		t.Fatalf("日志末行必须是 %s，实际日志: %q", want, log)
	}
}

// exitCodeOf 取出脚本的退出码，非 *exec.ExitError 直接失败。
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("应拿到 *exec.ExitError，实际 %v", err)
	}
	return exitErr.ExitCode()
}

// exitedChild 起一个立刻退出的子进程并 reap，返回其 PID。
func exitedChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	return cmd.Process.Pid
}

func TestScriptRunSuccessPath(t *testing.T) {
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	if _, err := runScript(t, p, exitedChild(t)); err != nil {
		t.Fatalf("脚本应成功: %v", err)
	}
	got, readErr := os.ReadFile(p.Target)
	if readErr != nil || !strings.HasPrefix(string(got), "#!/bin/sh") {
		t.Fatalf("正式二进制内容错误: %q %v", got, readErr)
	}
	if _, err := os.Stat(p.Pending); !os.IsNotExist(err) {
		t.Fatal("成功路径 pending 必须已被消费")
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.6.0")); err != nil {
		t.Fatalf("备份缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.5.0")); !os.IsNotExist(err) {
		t.Fatal("更早的备份必须被清理（只留最近一份）")
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); !os.IsNotExist(err) {
		t.Fatal("成功路径脚本必须自删")
	}
	// 成功路径会删除日志（spec §8.3 第 9 步），所以只能断言它不存在，不能断言内容
	if _, err := os.Stat(p.LogPath); !os.IsNotExist(err) {
		t.Fatal("成功路径日志必须被删除")
	}
	assertLockKept(t, p)
}

func TestScriptRunCleanupKeepsPending(t *testing.T) {
	// 这条专门挡住「比较绝对路径与 glob 裸名恒为假 → 删掉 pending」的缺陷（spec §16.2 B2）。
	// 注意（Task 6 fix round 1 之后的行为变更）：--backup 父目录不存在现在会在参数预检被判为
	// args 类失败（退 2），因此本用例不会到达第 4 步。「第 4 步之后 pending 仍存在」的覆盖由
	// TestScriptRunCleanupKeepsPendingAfterStep4 与 script_test.go 的 TestScriptBackupNotWritable 承担；
	// 本用例保留以固化「失败路径不得删 pending / 不得动 TARGET」这一不变量。
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	p.Backup = filepath.Join(t.TempDir(), "missing-dir", "sshore.v0.6.0")
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("备份失败必须报错, log=%s", log)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Fatalf("backup 父目录不存在属 args 类失败，必须退 2，实际 %d（log=%s）", code, log)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("pending 不能被清理掉: %v", err)
	}
	if got, _ := os.ReadFile(p.Target); string(got) != "OLD\n" {
		t.Fatalf("失败时正式二进制必须保持旧内容: %q", got)
	}
	assertLockKept(t, p)
}

func TestScriptRunCleanupKeepsPendingAfterStep4(t *testing.T) {
	// spec §13.2 第 2 条 + §16.2 B2：第 4 步清理确实执行（更早备份被删），但 pending 必须留下。
	// 用「backup 父目录存在但只读」让第 5 步失败：这样第 4 步一定跑过，事后可验证。
	if os.Geteuid() == 0 {
		t.Skip("root 有 CAP_DAC_OVERRIDE，0500 构造不出只读目录")
	}
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	roDir := filepath.Join(p.ExeDir, "ro-backup")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	// 先恢复写权限，t.TempDir 的递归清理才能删掉这个目录（Cleanup 为 LIFO，早于它执行）。
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
	p.Backup = filepath.Join(roDir, "sshore.v0.6.0")

	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("第 5 步备份失败必须报错, log=%s", log)
	}
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("备份失败必须退 3，实际 %d（log=%s）", code, log)
	}
	assertLastResult(t, log, "RESULT=fail:5")
	// 第 4 步跑过的证据：更早的备份（sshore.v0.5.0）已被清理。
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.5.0")); !os.IsNotExist(err) {
		t.Fatalf("第 4 步应清理更早备份: %v", err)
	}
	// 第 4 步绝不能删掉 pending（B2 回归保护）。
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("第 4 步不得删 pending: %v", err)
	}
	// 不变量：第 5 步失败也要让正式名保持旧二进制（应用不得消失）。
	got, readErr := os.ReadFile(p.Target)
	if readErr != nil || string(got) != "OLD\n" {
		t.Fatalf("备份失败后 TARGET 必须是旧二进制: %q %v", got, readErr)
	}
	assertLockKept(t, p)
}

func TestScriptRunSizeMismatchKeepsTarget(t *testing.T) {
	// 裁定 2：第 5 步之前的尺寸不符（第 3 步）也必须保证 TARGET 在位且内容为旧二进制。
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size() + 1
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("尺寸不符必须失败, log=%s", log)
	}
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("尺寸不符必须退 3，实际 %d（log=%s）", code, log)
	}
	if !strings.Contains(log, "大小不符") {
		t.Fatalf("日志应含大小不符原因: %s", log)
	}
	assertLastResult(t, log, "RESULT=fail:3")
	got, readErr := os.ReadFile(p.Target)
	if readErr != nil || string(got) != "OLD\n" {
		t.Fatalf("尺寸不符后 TARGET 必须是旧二进制: %q %v", got, readErr)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("尺寸不符必须保留 pending: %v", err)
	}
	assertLockKept(t, p)
}

func TestScriptRunWaitTimeoutKeepsEverything(t *testing.T) {
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	p.Wait = time.Second
	// 用一个长存活子进程当「旧进程」：它在整个等待期内一直活着，kill -0 才会持续成功，
	// 从而用短的 --wait 1 稳定触发超时（不依赖 setsid 的 fork 语义）。由 t.Cleanup kill+Wait 收尾。
	sleeper := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	pid := sleeper.Process.Pid
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	log, err := runScript(t, p, pid)
	if err == nil {
		t.Fatal("等待超时必须失败")
	}
	if !strings.Contains(log, "RESULT=fail:wait") {
		t.Fatalf("日志应含 RESULT=fail:wait: %s", log)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("超时必须保留 pending: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); err != nil {
		t.Fatalf("超时必须保留脚本（供重试）: %v", err)
	}
	assertLastResult(t, log, "RESULT=fail:wait")
	// 裁定 2：等待超时（第 2 步，早于第 5 步）结束后 TARGET 必须仍在且内容为旧二进制。
	got, readErr := os.ReadFile(p.Target)
	if readErr != nil || string(got) != "OLD\n" {
		t.Fatalf("超时后 TARGET 必须是旧二进制: %q %v", got, readErr)
	}
	assertLockKept(t, p)
}

func TestScriptRunLaunchFailureRollsBack(t *testing.T) {
	// 新版起不来（用立刻退出的假二进制模拟）：脚本必须回滚并保留 pending（spec §8.3）。
	p := newFixture(t)
	if err := os.WriteFile(p.Pending, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("新版起不来必须失败, log=%s", log)
	}
	if !strings.Contains(log, "RESULT=fail:launch") {
		t.Fatalf("日志应含 RESULT=fail:launch: %s", log)
	}
	if got, _ := os.ReadFile(p.Target); string(got) != "OLD\n" {
		t.Fatalf("回滚后正式二进制必须是旧内容: %q", got)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("回滚后 pending 必须保留（供重试）: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.6.0")); !os.IsNotExist(err) {
		t.Fatal("回滚后备份必须已改回正式名")
	}
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("启动失败必须退 3，实际 %d（log=%s）", code, log)
	}
	assertLastResult(t, log, "RESULT=fail:launch")
	assertLockKept(t, p)
}

func TestScriptRunSelfDeletesWhenInvokedByRelativePath(t *testing.T) {
	// 计划里的脚本会在替换前 cd 到目标目录；若 $0 是相对路径，朴素的 rm -f "$0" 会静默失败，
	// 留下一个陈旧的 sshore-update.sh（本计划实测过这个 bug），所以脚本必须用 $SELF 绝对路径自删。
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.ExeDir, "sshore-update.sh"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	// 用相对文件名 + cmd.Dir 调用，模拟「相对路径」场景。
	// 注：Go 不允许「固定实参 + 切片展开」混用在同一个可变参数上（brief 原文
	// exec.Command("sh", "sshore-update.sh", ScriptArgs(...)...) 无法编译），故先拼 argv。
	argv := append([]string{"sshore-update.sh"}, ScriptArgs(p, exitedChild(t))...)
	cmd := exec.Command("sh", argv...)
	cmd.Dir = p.ExeDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("脚本应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); !os.IsNotExist(err) {
		t.Fatal("相对路径调用时脚本也必须自删（用 $SELF）")
	}
	assertLockKept(t, p)
}

func TestScriptRunArgsValidation(t *testing.T) {
	// 裁定 1：args 类失败（--target 不存在 / --backup 父目录不存在 / --pid 非数字）必须
	// 写末行 RESULT=fail:args 并退 2；三种情形都要钉死。
	// 注：script_test.go 的 TestScriptArgsValidation 覆盖同一契约；本文件按 Task 12 的
	// runScript 夹具独立再跑一遍（两个文件互不共享 helper，避免互相遮蔽）。
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
				return ScriptArgs(p, exitedChild(t))
			},
		},
		{
			name:    "backup 父目录不存在",
			wantErr: "ERR=backup 父目录不存在",
			args: func(t *testing.T, p Plan) []string {
				p.Backup = filepath.Join(t.TempDir(), "missing-dir", "sshore.v0.6.0")
				return ScriptArgs(p, exitedChild(t))
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
			p := newFixture(t)
			st, _ := os.Stat(p.Pending)
			p.Size = st.Size()
			log, err := runScriptArgv(t, p, tc.args(t, p)...)
			if err == nil {
				t.Fatalf("参数非法必须失败, log=%s", log)
			}
			if code := exitCodeOf(t, err); code != 2 {
				t.Fatalf("参数类失败必须退 2，实际 %d（log=%s）", code, log)
			}
			if !strings.Contains(log, tc.wantErr) {
				t.Fatalf("日志应含 %q: %s", tc.wantErr, log)
			}
			assertLastResult(t, log, "RESULT=fail:args")
			// 参数预检失败时正式名（真实 TARGET）必须保持旧内容。
			got, readErr := os.ReadFile(filepath.Join(p.ExeDir, "sshore"))
			if readErr != nil || string(got) != "OLD\n" {
				t.Fatalf("参数失败后正式名必须是旧二进制: %q %v", got, readErr)
			}
			assertLockKept(t, p)
		})
	}
}
