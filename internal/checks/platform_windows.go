//go:build windows

package checks

import "golang.org/x/sys/windows"

// Elevated reports whether the process runs with an elevated token.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func freeBytes(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
