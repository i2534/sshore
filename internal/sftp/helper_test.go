package sftp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sshore/internal/osutil"
)

// —— Windows 可移植的「假子进程」夹具（Task 17 修复波 P1）——
//
// 修复前：多处测试用 osutil.StartPipes("cat") / StartPipes("sh", "-c", …) 造一个
// 「真实但本地的替身子进程」，只为满足 Session.Proc 的两条契约（close 会关管道并 Kill，
// 且带 Proc 的会话才算「真的建立过连接」）。Windows 客户机上没有 cat/sh，这些用例全红，
// 并级联出 10s 挂起（"10s 内未写出首块"）与误导性断言（"失败后必须保留恰好一个 .part"）。
//
// 修复后：替身子进程 = **重新执行本测试二进制**，由 helperTestMain 在 helper 模式下扮演
// cat（占住 stdin/stdout 直到父端关闭管道）。跨平台、仍是真实子进程（PipedProcess.Kill /
// Wait / StderrText 全部走真实实现），Linux 覆盖不减。sha256 之类「内容」用例不依赖它。
const (
	// helperEnvMode 是 helper 模式开关：非空时测试二进制不跑测试，直接扮演子进程。
	helperEnvMode = "SSHORE_TEST_HELPER_PROC"
	// helperEnvNoise 是写进 stderr 的一行「远端噪音」（供错误归属用例构造非空 StderrText）。
	helperEnvNoise = "SSHORE_TEST_HELPER_NOISE"
)

// helperTestMain 在 helper 模式下扮演一个长驻子进程；正常模式下先导出 helper 环境变量
// 再跑测试 —— 这样任何 osutil.StartPipes 起出来的后代进程（环境变量被继承）都会进入
// helper 模式，而不是递归再跑一遍整个测试套件。
func helperTestMain(m *testing.M) int {
	if mode := os.Getenv(helperEnvMode); mode != "" {
		if noise := os.Getenv(helperEnvNoise); noise != "" {
			_, _ = fmt.Fprintln(os.Stderr, noise)
		}
		// 像 cat 一样占住 stdin/stdout：阻塞读到父端关闭管道（EOF）后退出。
		buf := make([]byte, 4096)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				return 0
			}
		}
	}
	if err := os.Setenv(helperEnvMode, "idle"); err != nil {
		fmt.Fprintf(os.Stderr, "设置 %s 失败: %v\n", helperEnvMode, err)
		return 1
	}
	return m.Run()
}

// skipUnlessUnixDirPerms 是「只读父目录」类用例的守卫。
//
// 这类用例靠 unix 目录权限位（0500）让创建文件失败：Windows 根本没有这套语义
// （os.Chmod 只切只读属性，不影响在目录里建文件），所以必须显式 Skip 而不是让它
// 在真机上以「期待失败却成功」的形态假红。root 下 CAP_DAC_OVERRIDE 会绕过权限位，
// 同样跳过（两个平台约定一致：绝不静默断言一个恒真的东西）。
func skipUnlessUnixDirPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 unix 目录权限位语义（chmod 0500 不阻止在目录内建文件）")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 下 CAP_DAC_OVERRIDE 会绕过目录权限，0500 不构成只读")
	}
}

// testHelperPipes 起一个「跨平台 cat 替身」子进程，可选地先往 stderr 写一行噪音。
// 调用方负责在用例结束时 Close（现有的 noisyProcForDial / t.Cleanup 已经这么做）。
//
// 必须把 os.Args[0] 解析成**绝对路径**：Windows 上 CreateProcess 拒绝执行
// 「相对当前目录」找到的可执行文件（真机实测报 "cannot run executable found relative to
// current directory"），而 go test / 手工运行 test 二进制时 os.Args[0] 都可能是相对路径。
func testHelperPipes(t *testing.T, noise string) *osutil.PipedProcess {
	t.Helper()
	if noise != "" {
		t.Setenv(helperEnvNoise, noise)
	}
	self := os.Args[0]
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}
	pp, err := osutil.StartPipes(self)
	if err != nil {
		t.Fatalf("起跨平台测试替身子进程失败（%s=%q，exe=%q）: %v", helperEnvMode, os.Getenv(helperEnvMode), self, err)
	}
	return pp
}
