//go:build !windows

package adminops

import (
	"os"
	"syscall"
)

// private reports whether only this user can use the directory.
func private(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid() && fi.Mode().Perm()&0o077 == 0
}
