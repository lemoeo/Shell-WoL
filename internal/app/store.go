package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

type deviceFile struct {
	Devices []Device `yaml:"devices"`
}
type eventFile struct {
	Events []Event `yaml:"events"`
}
type Store struct {
	mu                    sync.RWMutex
	dir                   string
	config                Config
	devices               []Device
	events                []Event
	configDoc, devicesDoc *yaml.Node
}

func OpenStore(dir string) (s *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// A lock directory is portable, including Windows and embedded Linux.
	lockPath := filepath.Join(dir, ".lock")
	if err = os.Mkdir(lockPath, 0700); err != nil {
		return nil, fmt.Errorf("数据目录已被占用（异常退出后确认无进程再删除 %s）: %w", lockPath, err)
	}
	s = &Store{dir: dir, config: DefaultConfig(), devices: []Device{}, events: []Event{}}
	opened := s
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	configPath := filepath.Join(dir, "config.yaml")
	s.configDoc, err = readYAML(configPath, &s.config)
	if errors.Is(err, os.ErrNotExist) {
		s.configDoc, err = saveYAML(configPath, nil, s.config)
	}
	if err != nil {
		return nil, fmt.Errorf("config.yaml: %w", err)
	}
	if err = s.config.Validate(); err != nil {
		return nil, err
	}
	if s.config.PasswordHash != "" {
		if _, err = bcrypt.Cost([]byte(s.config.PasswordHash)); err != nil {
			return nil, errors.New("config.yaml 的 password_hash 无效")
		}
	}
	df := deviceFile{Devices: []Device{}}
	devicePath := filepath.Join(dir, "devices.yaml")
	s.devicesDoc, err = readYAML(devicePath, &df)
	if errors.Is(err, os.ErrNotExist) {
		s.devicesDoc, err = saveYAML(devicePath, nil, df)
	}
	if err != nil {
		return nil, fmt.Errorf("devices.yaml: %w", err)
	}
	if field(s.devicesDoc, "devices") == nil {
		return nil, errors.New("devices.yaml 缺少 devices 列表")
	}
	if len(df.Devices) > 1000 {
		return nil, errors.New("最多保存 1000 台设备")
	}
	seen := map[string]bool{}
	for i := range df.Devices {
		if err = df.Devices[i].Validate(); err != nil {
			return nil, err
		}
		if seen[df.Devices[i].ID] {
			return nil, errors.New("设备 ID 重复")
		}
		seen[df.Devices[i].ID] = true
	}
	s.devices = df.Devices
	var ef eventFile
	_, err = readYAML(filepath.Join(dir, "events.yaml"), &ef)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("events.yaml: %w", err)
	}
	s.events = ef.Events
	if len(s.events) > s.config.LogLimit {
		s.events = s.events[len(s.events)-s.config.LogLimit:]
	}
	err = nil
	return s, nil
}
func (s *Store) Close() error { return os.Remove(filepath.Join(s.dir, ".lock")) }
func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.config
	c.TrustedProxies = append([]string(nil), c.TrustedProxies...)
	return c
}
func (s *Store) Devices() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := append([]Device{}, s.devices...)
	credentials, _ := readCredentials(s.dir)
	for i := range list {
		r, ok := credentials.Credentials[list[i].ID]
		list[i].PasswordConfigured = ok && r.Binding == credentialBinding(list[i])
	}
	return list
}
func (s *Store) Get(id string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.devices {
		if d.ID == id {
			c, _ := readCredentials(s.dir)
			r, ok := c.Credentials[d.ID]
			d.PasswordConfigured = ok && r.Binding == credentialBinding(d)
			return d, true
		}
	}
	return Device{}, false
}
func (s *Store) SaveDevice(d Device, create bool) error {
	return s.SaveDeviceCredentials(d, create, nil, false)
}

func (s *Store) SaveDeviceCredentials(d Device, create bool, password []byte, clearPassword bool) error {
	if clearPassword && len(password) > 0 {
		return errors.New("不能同时清除和设置密码")
	}
	d.PasswordConfigured = false
	if err := d.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append([]Device{}, s.devices...)
	index := -1
	for i, v := range list {
		if v.ID == d.ID {
			index = i
			break
		}
	}
	if create && index >= 0 {
		return errors.New("设备 ID 已存在")
	}
	if !create && index < 0 {
		return errors.New("设备不存在")
	}
	if index < 0 {
		if len(list) >= 1000 {
			return errors.New("最多保存 1000 台设备")
		}
		list = append(list, d)
	} else {
		list[index] = d
	}
	credentials, err := readCredentials(s.dir)
	if err != nil {
		return err
	}
	prior := credentialFile{Version: 1, Credentials: map[string]credentialRecord{}}
	for id, r := range credentials.Credentials {
		prior.Credentials[id] = r
	}
	record, configured := credentials.Credentials[d.ID]
	changed := clearPassword || len(password) > 0 || (configured && record.Binding != credentialBinding(d))
	if configured && record.Binding != credentialBinding(d) && d.UsesVNC() && !clearPassword && len(password) == 0 {
		return errors.New("目标 IP、端口或用户名已改变，请重新设置 Mac VNC 密码")
	}
	if d.UsesVNC() && !configured && len(password) == 0 && !clearPassword {
		return errors.New("请设置 Mac VNC 密码")
	}
	if len(password) > 0 {
		if !d.UsesVNC() {
			return errors.New("请先选择 Mac VNC 唤醒方式")
		}
		if err = setCredential(s.dir, &credentials, d, password); err != nil {
			return err
		}
	} else if changed {
		delete(credentials.Credentials, d.ID)
	}
	if changed {
		if err = saveCredentials(s.dir, credentials); err != nil {
			return err
		}
	}
	if err = s.saveDevices(list); err != nil {
		if changed {
			if rollback := saveCredentials(s.dir, prior); rollback != nil {
				return errors.New("设备保存失败且凭据恢复失败，请检查数据目录")
			}
		}
		return err
	}
	return nil
}
func (s *Store) DeleteDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.devices {
		if d.ID == id {
			list := append([]Device{}, s.devices[:i]...)
			list = append(list, s.devices[i+1:]...)
			c, err := readCredentials(s.dir)
			if err != nil {
				return err
			}
			old, exists := c.Credentials[id]
			if exists {
				delete(c.Credentials, id)
				if err = saveCredentials(s.dir, c); err != nil {
					return err
				}
			}
			if err = s.saveDevices(list); err != nil {
				if exists {
					c.Credentials[id] = old
					if rollback := saveCredentials(s.dir, c); rollback != nil {
						return errors.New("删除失败且凭据恢复失败，请检查数据目录")
					}
				}
				return err
			}
			return nil
		}
	}
	return errors.New("设备不存在")
}
func (s *Store) saveDevices(list []Device) error {
	n, e := saveYAML(filepath.Join(s.dir, "devices.yaml"), s.devicesDoc, deviceFile{list})
	if e == nil {
		s.devicesDoc = n
		s.devices = list
	}
	return e
}
func (s *Store) SaveConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.PasswordHash = s.config.PasswordHash
	// Proxy trust is an administrator-only YAML setting, absent from the web API.
	c.TrustedProxies = append([]string(nil), s.config.TrustedProxies...)
	return s.saveConfig(c)
}
func (s *Store) saveConfig(c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	n, e := saveYAML(filepath.Join(s.dir, "config.yaml"), s.configDoc, c)
	if e == nil {
		s.configDoc = n
		s.config = c
	}
	return e
}
func (s *Store) SetPassword(password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("密码须为 12–72 字节")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.config
	c.PasswordHash = string(hash)
	return s.saveConfig(c)
}
func (s *Store) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Event{}
	limit := s.config.LogLimit
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.events[i])
	}
	return out
}
func (s *Store) Record(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.config.LogLimit
	if limit == 0 {
		return nil
	}
	events := append(append([]Event{}, s.events...), e)
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	if _, err := saveYAML(filepath.Join(s.dir, "events.yaml"), nil, eventFile{events}); err != nil {
		return err
	}
	s.events = events
	return nil
}
func (s *Store) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return yaml.Marshal(deviceFile{Devices: s.devices})
}

// ReadWakeDefaults reads atomic snapshots without taking the writer lock, so CLI
// sends can coexist with the Web service. CLI activity goes to its own output.
func ReadWakeDefaults(dir, id string) (Config, Device, error) {
	c := DefaultConfig()
	_, err := readYAML(filepath.Join(dir, "config.yaml"), &c)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, Device{}, err
	}
	if err = c.Validate(); err != nil {
		return c, Device{}, err
	}
	if id == "" {
		return c, Device{}, nil
	}
	var df deviceFile
	if _, err = readYAML(filepath.Join(dir, "devices.yaml"), &df); err != nil {
		return c, Device{}, err
	}
	for _, d := range df.Devices {
		if d.ID == id {
			if err = d.Validate(); err != nil {
				return c, Device{}, err
			}
			return c, d, nil
		}
	}
	return c, Device{}, errors.New("设备不存在")
}

func (s *Store) WakeSnapshot(id string) (Device, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.devices {
		if d.ID == id {
			if !d.UsesVNC() {
				return d, nil, nil
			}
			p, e := readDevicePassword(s.dir, d)
			return d, p, e
		}
	}
	return Device{}, nil, errors.New("设备不存在")
}

// CLI reads atomic files without opening another writer store.
func ReadDevicePassword(dir string, d Device) ([]byte, error) { return readDevicePassword(dir, d) }
