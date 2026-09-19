//go:build windows

package update

import "golang.org/x/sys/windows"

func FreeSpace(dir string) (int64, error) {
	var free, total, avail uint64
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err
	}
	return int64(free), nil
}
