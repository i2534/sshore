// internal/preset/platform_windows.go
//go:build windows

package preset

import "golang.org/x/sys/windows"

// knownDirs 用 KnownFolderPath 取真实路径：这几个目录可能被 OneDrive 重定向
// 或用户手工移动，直接拼 %USERPROFILE%\Desktop 会给出不存在的预设。
func knownDirs() map[string]string {
	out := map[string]string{}
	for key, id := range map[string]*windows.KNOWNFOLDERID{
		"Desktop":   windows.FOLDERID_Desktop,
		"Downloads": windows.FOLDERID_Downloads,
		"Documents": windows.FOLDERID_Documents,
	} {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil && p != "" {
			out[key] = p
		}
	}
	return out
}

// rootPresets：Windows 的"根"按盘符给（见 Drives），不再重复一个 "/"。
func rootPresets() []Preset { return nil }

func logicalDriveMask() uint32 {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return 0
	}
	return mask
}
