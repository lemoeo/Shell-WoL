package app

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryFileLock(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrDataLocked
	}
	return err
}

func execWithDataLock(_ *os.File, _ []string) error {
	return errors.New("Windows 的 lock 命令仅支持锁检查；外部维护命令仅用于 POSIX 安装脚本")
}
