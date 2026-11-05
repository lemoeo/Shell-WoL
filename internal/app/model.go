package app

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type Config struct {
	PublicURL    string `yaml:"public_url" json:"public_url"`
	Listen       string `yaml:"listen" json:"listen"`
	PasswordHash string `yaml:"password_hash" json:"-"`
	Broadcast    string `yaml:"broadcast" json:"broadcast"`
	Port         int    `yaml:"port" json:"port"`
	Repeat       int    `yaml:"repeat" json:"repeat"`
	LogLimit     int    `yaml:"log_limit" json:"log_limit"`
}

func DefaultConfig() Config {
	return Config{Listen: "127.0.0.1:8080", Broadcast: "255.255.255.255", Port: 9, Repeat: 3, LogLimit: 100}
}
func (c Config) Validate() error {
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("外部访问地址须为 http(s)://主机[:端口]，不能含路径、凭据或查询参数")
		}
	}

	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("监听地址: %w", err)
	}
	if err := destination(c.Broadcast); err != nil {
		return err
	}
	if c.Port < 1 || c.Port > 65535 || c.Repeat < 1 || c.Repeat > 10 || c.LogLimit < 0 || c.LogLimit > 1000 {
		return errors.New("端口须为 1–65535，重复次数 1–10，记录上限 0–1000")
	}
	return nil
}

type Device struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	MAC       string `yaml:"mac" json:"mac"`
	Broadcast string `yaml:"broadcast" json:"broadcast"`
	Port      int    `yaml:"port" json:"port"`
	Repeat    int    `yaml:"repeat" json:"repeat"`
	Interface string `yaml:"interface" json:"interface"`
	IP        string `yaml:"ip" json:"ip"`
	ProbePort int    `yaml:"probe_port" json:"probe_port"`
	Note      string `yaml:"note" json:"note"`
}

func (d *Device) Validate() error {
	d.Name = strings.TrimSpace(d.Name)
	if len(d.ID) < 1 || len(d.ID) > 64 || strings.IndexFunc(d.ID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 {
		return errors.New("设备 ID 仅支持 1–64 位字母、数字、下划线和连字符")
	}
	if d.Name == "" || len(d.Name) > 120 || len(d.Note) > 2000 {
		return errors.New("名称不能为空且不超过 120 字节，备注不超过 2000 字节")
	}
	mac, err := net.ParseMAC(d.MAC)
	if err != nil || len(mac) != 6 {
		return errors.New("MAC 必须是 6 字节地址，例如 AA:BB:CC:DD:EE:FF")
	}
	d.MAC = strings.ToUpper(mac.String())
	if err := destination(d.Broadcast); err != nil {
		return err
	}
	if d.Port < 1 || d.Port > 65535 || d.Repeat < 1 || d.Repeat > 10 {
		return errors.New("端口须为 1–65535，重复次数 1–10")
	}
	if len(d.Interface) > 128 {
		return errors.New("网卡名称过长")
	}
	if d.IP != "" {
		ip := net.ParseIP(d.IP)
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("检测 IP 必须是有效的单播 IP 地址")
		}
	}
	if d.ProbePort < 0 || d.ProbePort > 65535 {
		return errors.New("检测端口须为 1–65535，0 表示未配置")
	}
	return nil
}
func destination(s string) error {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("广播目标必须是有效的 IPv4 地址")
	}
	return nil
}

type Event struct {
	Time     string `yaml:"time" json:"time"`
	DeviceID string `yaml:"device_id" json:"device_id"`
	Name     string `yaml:"name" json:"name"`
	Result   string `yaml:"result" json:"result"`
}
