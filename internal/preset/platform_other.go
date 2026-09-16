//go:build !windows

package preset

// knownDirs：非 Windows 没有"已知文件夹"API，交给 Local 用 home + 英文目录名兜底。
func knownDirs() map[string]string { return nil }

// rootPresets：非 Windows 的根就是 "/"。
func rootPresets() []Preset { return []Preset{{Name: "根目录", Path: "/"}} }

// logicalDriveMask：盘符是 Windows 概念，其他平台恒为 0（Drives 因此返回空组）。
func logicalDriveMask() uint32 { return 0 }
