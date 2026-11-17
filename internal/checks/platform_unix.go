//go:build unix

package checks

import (
	"os"
	"syscall"
)

// Elevated reports whether the process runs as root.
func Elevated() bool { return os.Geteuid() == 0 }

func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // field types differ between systems
}
