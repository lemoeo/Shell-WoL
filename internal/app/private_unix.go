//go:build !windows

package app

import "os"

func protectPrivateFile(path string) error { return os.Chmod(path, 0600) }
