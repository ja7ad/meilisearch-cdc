package adminops

import "os"

// private: the temp directory is already per user on Windows.
func private(os.FileInfo) bool { return true }
