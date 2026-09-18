package osutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— Windows 可移植的替身进程（Task 17 修复波）——
//
// osutil 里有多条以 Unix shell 为夹具的用例。Windows（CI 的 go-windows job 与真机）没有
// sh，此前有的带 exec.LookPath("sh") 守卫、有的漏了，于是出现 4 条红。这里给出与
// internal/sftp 同思路的跨平台夹具：**重新执行测试二进制**，helper 模式按环境变量扮演替身，
// 仍是真实子进程（Process/PipedProcess 的 Close/Wait/StderrText 全部走生产实现）。
//
// 模式经环境变量传递：StartPipes 与 Spawner/Streamer 都只接受 name+args，但子进程会继承
// 父进程环境（helperPipesPath 用 t.Setenv 设置，测试间自动还原）。
const (
	osutilHelperEnv = "SSHORE_OSUTIL_HELPER"
	osutilArgsEnv   = "SSHORE_OSUTIL_ARGS"
)

// osutilTestMain：helper 模式下不跑测试，直接扮演替身进程。
//   - "cat"     ：把 stdin 原样写到 stdout（父端关闭 stdin 后 EOF 退出）；
//   - "streams" ：stdout 写 o1/o2，stderr 写 e1/"  e2  "（逐行回调用例断言）；
//   - "idle"    ：立刻以 0 退出（NewRunner 用例：只验"能跑起来且退出码 0"）；
//   - "exit N"  ：立刻以退出码 N 结束（StartStream 用例）。
func osutilTestMain(m *testing.M) int {
	mode := os.Getenv(osutilHelperEnv)
	if mode == "" {
		// 正常模式：先导出开关再跑测试 —— 这样 StartPipes 起的后代进程（环境被继承）会进入
		// helper 模式，而不是递归再跑一遍测试套件。
		if err := os.Setenv(osutilHelperEnv, "idle"); err != nil {
			fmt.Fprintf(os.Stderr, "设置 %s 失败: %v\n", osutilHelperEnv, err)
			return 1
		}
		return m.Run()
	}
	switch mode {
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "streams":
		_, _ = fmt.Fprintln(os.Stdout, "o1")
		_, _ = fmt.Fprintln(os.Stdout, "o2")
		_, _ = fmt.Fprintln(os.Stderr, "e1")
		_, _ = fmt.Fprintln(os.Stderr, "  e2  ")
	case "exit":
		// 退出码来源优先取 os.Args（如 sshore.test.exe exit 3），否则取 SSHORE_OSUTIL_ARGS。
		code := 0
		if len(os.Args) >= 2 {
			_, _ = fmt.Sscanf(strings.TrimSpace(os.Args[1]), "%d", &code)
		} else {
			_, _ = fmt.Sscanf(strings.TrimSpace(os.Getenv(osutilArgsEnv)), "%d", &code)
		}
		return code
	case "flood":
		// nil 回调不建管道 ⇒ 子进程写满 64KB 管道后必须仍能正常结束。写 1MB 到 stdout
		// （原 sh 用例的"多写点"意图），Windows 上不再依赖 /dev/zero。
		blob := []byte(strings.Repeat("x", 4096))
		for i := 0; i < 256; i++ {
			if _, err := os.Stdout.Write(blob); err != nil {
				return 1
			}
		}
		return 0
	case "idle":
		return 0
	default:
		_, _ = fmt.Fprintf(os.Stderr, "未知 helper 模式 %q\n", mode)
		return 2
	}
	return 0
}

// TestMain 让 osutil 测试二进制可以在 helper 模式下扮演替身进程。
func TestMain(m *testing.M) {
	os.Exit(osutilTestMain(m))
}

// helperExe 返回测试二进制的绝对路径（Windows 上 CreateProcess 拒绝相对路径）。
func helperExe(t *testing.T) string {
	t.Helper()
	self := os.Args[0]
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}
	return self
}
