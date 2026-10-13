//go:build windows

package clipboard

import (
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows（v0.47.0）不导出任何剪贴板/全局内存 API —— GlobalAlloc、
// OpenClipboard、SetClipboardData、CF_HDROP、GMEM_MOVEABLE 等全部 undefined
// （标准库 syscall 也没有），所以这里自建过程表。见设计 §4.1.1。
//
// 注意：wails 自带的 internal/.../winc/w32 有全套符号，但位于 internal/ 树内，
// 导入会被 Go 拒绝，只能当写法参照（本文件的写法即参照它）。
const (
	cfHDrop      = 15     // CF_HDROP
	gmemMoveable = 0x0002 // GMEM_MOVEABLE
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procCloseClipboard   = user32.NewProc("CloseClipboard")

	procGlobalAlloc   = kernel32.NewProc("GlobalAlloc")
	procGlobalLock    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock  = kernel32.NewProc("GlobalUnlock")
	procGlobalFree    = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

// Support 报告当前平台是否支持把文件/目录作为文件对象写入系统剪贴板。
func Support() bool { return true }

// SetFiles 把 paths 指向的文件/目录作为 CF_HDROP 写入系统剪贴板，
// 之后可在资源管理器里直接粘贴出副本。任一路径不存在时返回错误，
// 且**不触碰剪贴板**（校验发生在 OpenClipboard 之前）。
func SetFiles(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("没有要写入剪贴板的路径")
	}
	// ① 路径校验必须在打开剪贴板之前（真机测试才能在不污染剪贴板的前提下跑）。
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("路径不存在: %s", p)
		}
	}

	// ② OpenClipboard 与 CloseClipboard 必须落在同一线程，否则剪贴板死锁。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// ③ LazyProc.Call 在 proc 找不到时直接 panic（内部走 mustFind）⇒ 先 Find 转成
	//    普通 error，否则 panic 会被 Wails dispatcher 静默吞掉、界面毫无反应。
	procs := []*windows.LazyProc{
		procOpenClipboard, procEmptyClipboard, procSetClipboardData, procCloseClipboard,
		procGlobalAlloc, procGlobalLock, procGlobalUnlock, procGlobalFree, procRtlMoveMemory,
	}
	for _, p := range procs {
		if err := p.Find(); err != nil {
			return fmt.Errorf("找不到 Win32 过程 %s: %w", p.Name, err)
		}
	}

	data := buildHDropBytes(paths)

	// ④ GlobalAlloc(GMEM_MOVEABLE) → GlobalLock → 填内容 → GlobalUnlock。
	hMem, _, callErr := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc 失败: %v", callErr)
	}
	owned := true
	defer func() {
		// 只有"交接之前"失败才释放；SetClipboardData 成功后内存归系统所有。
		if owned {
			_, _, _ = procGlobalFree.Call(hMem)
		}
	}()

	ptr, _, callErr := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock 失败: %v", callErr)
	}
	// 用 RtlMoveMemory 做 memcpy：这里只有 Go 指针→uintptr 一个方向。
	// 若改用 unsafe.Slice((*byte)(unsafe.Pointer(ptr)), n)（uintptr→指针），
	// go vet 的 unsafeptr 会报 possible misuse of unsafe.Pointer，
	// 而 CI 的 go-windows job 跑的正是 go vet ./...。
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	// GlobalUnlock 是反向约定：返回 0 可能是"锁计数归零"的正常情况，
	// 必须配合 GetLastError 才判得出失败。
	if r, _, unlockErr := procGlobalUnlock.Call(hMem); r == 0 && windows.GetLastError() != nil {
		return fmt.Errorf("GlobalUnlock 失败: %v", unlockErr)
	}

	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()

	if r, _, err := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard 失败: %v", err)
	}
	if r, _, err := procSetClipboardData.Call(cfHDrop, hMem); r == 0 {
		return fmt.Errorf("SetClipboardData 失败: %v", err)
	}
	// 内存所有权已移交系统：抑制 deferred GlobalFree。
	owned = false
	return nil
}

// openClipboard 在剪贴板被其它进程占用时有限重试（最多约 1 秒）。
func openClipboard() error {
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for {
		r, _, err := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("OpenClipboard 失败（剪贴板可能被其它程序占用）: %v", lastErr)
		}
		time.Sleep(time.Millisecond)
	}
}
