//go:build linux || darwin

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func tryFileLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrDataLocked
	}
	return err
}

func execWithDataLock(f *os.File, args []string) error {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	// F_DUPFD leaves CLOEXEC clear, carrying flock through exec/fork even if
	// the calling installer is killed. Go's original descriptor stays CLOEXEC.
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD, 3)
	if err != nil {
		return fmt.Errorf("传递维护锁: %w", err)
	}
	defer unix.Close(fd)
	return unix.Exec(path, args, os.Environ())
}
