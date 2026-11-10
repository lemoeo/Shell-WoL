package app

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

var ErrAdminInitialized = errors.New("管理员已初始化，请使用密码登录")

type setupTokenFile struct {
	Instance string `json:"instance"`
	Token    string `json:"token"`
}

func (s *Store) clearSetupToken() error {
	path := filepath.Join(s.dir, ".setup-token")
	if err := regularCredentialFile(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(path)
}

// PublishSetupToken is called only after the HTTP listener has bound.
func (s *Store) PublishSetupToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	s.lock.info.Kind = "serve"
	if err := s.lock.writeRecord(); err != nil {
		return err
	}
	if s.config.PasswordHash != "" {
		return nil
	}
	b, err := json.Marshal(setupTokenFile{Instance: s.lock.info.ID, Token: token})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".setup-token-*.tmp")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = protectPrivateFile(path); err == nil {
		_, err = f.Write(b)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(s.dir, ".setup-token"))
}

func (s *Store) passwordTokenCleanup() {
	if err := s.clearSetupToken(); err != nil {
		slog.Warn("管理员密码已保存，但初始化令牌文件清理失败", "error", err)
	}
}

// ReadSetupToken is read-only: it never opens a Store or generates a token.
// A fresh instance ID plus the live kernel lock rule out stale crash/PID files.
func ReadSetupToken(dir string) (string, error) {
	var c Config
	if _, err := readYAML(filepath.Join(dir, "config.yaml"), &c); err != nil {
		return "", err
	}
	if c.PasswordHash != "" {
		return "", ErrAdminInitialized
	}
	f, err := openLockFile(dir, false)
	if err != nil {
		return "", fmt.Errorf("无法确认服务状态: %w", err)
	}
	defer f.Close()
	if err = tryFileLock(f); err == nil {
		return "", errors.New("服务未运行，请先启动服务")
	} else if !errors.Is(err, ErrDataLocked) {
		return "", err
	}
	before, err := readLockRecord(f)
	if err != nil || before.Kind != "serve" {
		return "", errors.New("当前没有可读取令牌的 Web 服务")
	}
	path := filepath.Join(dir, ".setup-token")
	if err = regularCredentialFile(path); err != nil {
		return "", errors.New("当前初始化令牌文件不可用，请检查服务状态")
	}
	tokenFile, err := os.Open(path)
	if err != nil {
		return "", err
	}
	var token setupTokenFile
	err = json.NewDecoder(io.LimitReader(tokenFile, 4096)).Decode(&token)
	tokenFile.Close()
	if err != nil {
		return "", errors.New("初始化令牌文件无效")
	}
	after, err := readLockRecord(f)
	raw, tokenErr := hex.DecodeString(token.Token)
	if err != nil || before != after || after.Kind != "serve" ||
		subtle.ConstantTimeCompare([]byte(after.ID), []byte(token.Instance)) != 1 ||
		tokenErr != nil || len(raw) != 32 {
		return "", errors.New("初始化令牌不属于当前服务，请重试")
	}
	if err = tryFileLock(f); err == nil {
		return "", errors.New("服务已经停止")
	} else if !errors.Is(err, ErrDataLocked) {
		return "", err
	}
	// Password setup may have completed during the file reads.
	if _, err = readYAML(filepath.Join(dir, "config.yaml"), &c); err != nil {
		return "", err
	}
	if c.PasswordHash != "" {
		return "", ErrAdminInitialized
	}
	return token.Token, nil
}
