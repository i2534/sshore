//go:build windows

package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setDetached 隐藏控制台窗口；子进程不随父进程退出（见 spec §8.5）。
// 常量取自 golang.org/x/sys/windows —— syscall 包没有 CREATE_NO_WINDOW（spec §4 F7）。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// StartDetached 分离启动脚本；参数经环境变量传递（spec §8.5）。
//
// 排障提示（2026-10-12 Windows 真机定位）：「重启并升级」的失败几乎都出在 update.cmd
// 的**编码**上，而不是进程启动方式 —— cmd.exe 用控制台代码页解码批处理文件，非 ASCII
// 字节（例如中文注释）会吞掉行结束符、令其后每一行错位，脚本在毫秒级以 EXIT=255 退出
// 且不写任何 STEP/RESULT，表现为「点重启并升级后应用消失、版本不变、pending 残留」。
// 因此 update.cmd 必须纯 ASCII（由 TestScriptBytesAreSafeAndLFOnly 门禁）。
//
// 该失败与 Job Object 无关：宿主进程虽然位于 WebView2 的 job 内（实测 IsProcessInJob==true），
// 但直接 exec 出的脚本能活过宿主退出 —— 编码修好后，直接 exec 与经 WMI 启动的替换
// 都能端到端成功（两种实现各在真机验过一次）。
func StartDetached(_ string, scriptPath string, _ []string, env []string) error {
	cmd := exec.Command("cmd", "/d", "/c", scriptPath)
	cmd.Env = append(cmd.Environ(), env...)
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
