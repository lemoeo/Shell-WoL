package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// CheckData validates without taking a writer lock or changing any files.
func CheckData(dir string, requirePassword, running bool) error {
	c := DefaultConfig()
	if _, err := readYAML(filepath.Join(dir, "config.yaml"), &c); err != nil {
		return fmt.Errorf("config.yaml: %w", err)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.PasswordHash != "" {
		if _, err := bcrypt.Cost([]byte(c.PasswordHash)); err != nil {
			return errors.New("管理员密码哈希无效")
		}
	} else if requirePassword {
		return errors.New("管理员密码尚未初始化")
	}
	var df deviceFile
	n, err := readYAML(filepath.Join(dir, "devices.yaml"), &df)
	if err != nil {
		return fmt.Errorf("devices.yaml: %w", err)
	}
	if field(n, "devices") == nil || len(df.Devices) > 1000 {
		return errors.New("devices.yaml 缺少设备列表或超过 1000 台")
	}
	seen := map[string]bool{}
	for i := range df.Devices {
		d := &df.Devices[i]
		if err = d.Validate(); err != nil {
			return err
		}
		if seen[d.ID] {
			return errors.New("设备 ID 重复")
		}
		seen[d.ID] = true
	}
	var ef eventFile
	if _, err = readYAML(filepath.Join(dir, "events.yaml"), &ef); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("events.yaml: %w", err)
	}
	credentials, err := readCredentials(dir)
	if err != nil {
		return err
	}
	if len(credentials.Credentials) > 0 {
		key, e := credentialKey(dir, false)
		if e != nil {
			return e
		}
		defer clear(key)
		for _, d := range df.Devices {
			if record, ok := credentials.Credentials[d.ID]; ok {
				if record.Binding != credentialBinding(d) {
					return errors.New("设备目标与凭据不匹配，请重新配置 Mac 密码")
				}
				secret, e := decryptCredential(record, key)
				if e != nil {
					return e
				}
				clear(secret)
			}
		}
	}
	if !running {
		return nil
	}
	host, port, _ := net.SplitHostPort(c.Listen)
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/api/session", nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("服务接口不可达: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("服务接口返回 HTTP %d", response.StatusCode)
	}
	var session struct {
		Authenticated *bool `json:"authenticated"`
		SetupRequired *bool `json:"setup_required"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, response.Body, 4096)).Decode(&session); err != nil {
		return err
	}
	if session.Authenticated == nil || session.SetupRequired == nil {
		return errors.New("响应不是 Shell-WoL 会话接口")
	}
	if requirePassword && *session.SetupRequired {
		return errors.New("运行中的服务尚未初始化密码")
	}
	return nil
}
