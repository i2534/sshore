//go:build !windows

package clipboard

import "errors"

// ErrUnsupported 表示当前平台不支持把文件/目录作为文件对象写入系统剪贴板。
//
// Linux 要让文件管理器粘贴出文件，剪贴板必须由持有 selection 的进程提供
// text/uri-list / x-special/gnome-copied-files 这类"文件对象"格式；WebKit 的
// 剪贴板 API 写不了该格式，需要 cgo + GTK3。按用户决策本项目不做（设计 §4.4），
// 因此 Linux 上调用 SetFiles 只会拿到这个错误，前端据 Support() 直接不渲染该项。
var ErrUnsupported = errors.New("当前平台不支持把文件写入系统剪贴板")

// Support 报告当前平台是否支持把文件/目录作为文件对象写入系统剪贴板。
func Support() bool { return false }

// SetFiles 在非 Windows 平台上始终返回 ErrUnsupported（不静默成功）。
func SetFiles(paths []string) error { return ErrUnsupported }
