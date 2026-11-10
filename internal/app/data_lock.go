package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var ErrDataLocked = errors.New("数据目录正在使用")

type lockRecord struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// The file remains on disk. Byte zero is reserved for Windows locking;
// non-secret instance metadata starts at byte one so readers can inspect it.
type DataLock struct {
	file *os.File
	info lockRecord
	once sync.Once
	err  error
}

func openLockFile(dir string, create bool) (*os.File, error) {
	path := filepath.Join(dir, ".lock")
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		if info.IsDir() {
			return nil, fmt.Errorf("发现旧版目录锁 %s；请先停止旧程序并确认无进程使用，再用 rmdir 清理空目录一次", path)
		}
		return nil, errors.New("数据锁必须是普通文件，不能是符号链接")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err = sameLockFile(f, path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func sameLockFile(f *os.File, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("数据锁文件发生变化，请重试")
	}
	return nil
}

func AcquireDataLock(dir string) (*DataLock, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := openLockFile(dir, true)
	if err != nil {
		return nil, err
	}
	if err = tryFileLock(f); err != nil {
		f.Close()
		return nil, err
	}
	lock := &DataLock{file: f, info: lockRecord{ID: randomToken(), Kind: "maintenance"}}
	// Reject a descriptor detached from this path by a completed purge.
	if err = sameLockFile(f, filepath.Join(dir, ".lock")); err == nil {
		err = protectPrivateFile(filepath.Join(dir, ".lock"))
	}
	if err == nil {
		err = lock.writeRecord()
	}
	if err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func (l *DataLock) writeRecord() error {
	b, err := json.Marshal(l.info)
	if err != nil {
		return err
	}
	if err = l.file.Truncate(0); err != nil {
		return err
	}
	_, err = l.file.WriteAt(append([]byte{'\n'}, b...), 0)
	return err
}

func (l *DataLock) Close() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

// Run retains the kernel lock through exec and synchronous child commands.
func (l *DataLock) Run(args []string) error {
	if len(args) == 0 {
		return errors.New("维护命令不能为空")
	}
	return execWithDataLock(l.file, args)
}

func readLockRecord(f *os.File) (lockRecord, error) {
	var record lockRecord
	err := json.NewDecoder(io.NewSectionReader(f, 1, 4096)).Decode(&record)
	return record, err
}
