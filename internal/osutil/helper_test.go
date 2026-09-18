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
// sh，此前有的带 exec.LookPath("sh") 守卫、有的漏了，于是出现 5 条红。这里给出与
// internal/sftp 同思路的跨平台夹具：**重新执行测试二进制**，仍是真实子进程
// （Process/PipedProcess 的 Close/Wait/StderrText 全部走生产实现）。
//
// 两个开关都必须命中才进入 helper 模式：环境变量（由 helperPipes 等夹具在本测试内设置并
// 还原）+ argv[1] 是已知模式。这样 StartPipes 起的真实 sh 子进程（没设环境变量）与
// `go test` 自己起的测试子进程（argv 是 -test.*）都不会误入 helper —— 早期版本用
// "TestMain 全局导出环境变量"的写法，让 sh 子进程继承开关后立刻退出，把
// TestStartPipesDoesNotBlockWhenStderrUnread 变成假红（-race 下必现）。
//
//	helper.test.exe cat             → stdin 原样回显到 stdout（父端关 stdin 后退出）
//	helper.test.exe streams         → stdout o1/o2、stderr e1/"  e2  "
//	helper.test.exe flood           → 往 stdout 写 1MB（nil 回调不建管道用例）
//	helper.test.exe exit 3          → 立即以 3 退出
const osutilHelperEnv = "SSHORE_OSUTIL_HELPER"

// osutilTestMain：只有「环境变量 + argv 模式」同时命中才扮演替身，否则跑正常测试。
func osutilTestMain(m *testing.M) int {
	if os.Getenv(osutilHelperEnv) == "" {
		return m.Run() // go test 正常运行路径（含子进程）
	}
	mode := ""
	if len(os.Args) >= 2 {
		mode = os.Args[1]
	}
	knownMode := func(m string) bool {
		switch m {
		case "cat", "streams", "flood", "exit":
			return true
		}
		return false
	}
	if !knownMode(mode) {
		// 开关在但没有模式参数（例如被 StartPipes 以无参方式误触发）：**绝不能递归跑测试**
		// ——那会无限派生测试二进制。直接成功退出（NewRunner 用例正是"跑起来即成功"）。
		return 0
	}
	switch mode {
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "streams":
		_, _ = fmt.Fprintln(os.Stdout, "o1")
		_, _ = fmt.Fprintln(os.Stdout, "o2")
		_, _ = fmt.Fprintln(os.Stderr, "e1")
		_, _ = fmt.Fprintln(os.Stderr, "  e2  ")
	case "flood":
		blob := []byte(strings.Repeat("x", 4096))
		for i := 0; i < 256; i++ {
			if _, err := os.Stdout.Write(blob); err != nil {
				return 1
			}
		}
		return 0
	case "exit":
		code := 0
		if len(os.Args) >= 3 {
			_, _ = fmt.Sscanf(strings.TrimSpace(os.Args[2]), "%d", &code)
		}
		return code
	}
	return 0
}

// TestMain 让 osutil 测试二进制可以在 helper 模式下扮演替身进程。
func TestMain(m *testing.M) {
	os.Exit(osutilTestMain(m))
}

// helperExe 返回测试二进制的绝对路径，并打开 helper 开关。
//
// 开关必须**随本次测试设置并自动还原**（t.Setenv）：Spawner/Streamer 起的子进程继承父
// 进程环境，所以 Start 调用期间开关在；测试结束后即还原，后面用 sh 的用例绝不会继承它。
// 早期版本在 TestMain 里全局导出开关，sh 子进程继承后误入 helper 并立刻退出，
// TestStartPipesDoesNotBlockWhenStderrUnread 因此假红（-race 下必现，本轮 make ci 抓到）。
func helperExe(t *testing.T) string {
	t.Helper()
	t.Setenv(osutilHelperEnv, "1")
	self := os.Args[0]
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}
	return self
}

// helperPipes 用 StartPipes 起一个 helper 子进程（StartPipes 路径的覆盖）。
func helperPipes(t *testing.T, mode string, args ...string) *PipedProcess {
	t.Helper()
	self := helperExe(t) // 打开开关
	p, err := StartPipes(self, append([]string{mode}, args...)...)
	if err != nil {
		t.Fatalf("起 osutil helper 子进程失败（mode=%q）: %v", mode, err)
	}
	return p
}
