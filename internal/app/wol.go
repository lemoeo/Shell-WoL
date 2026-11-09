package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, errors.New("无效的 6 字节 MAC 地址")
	}
	p := make([]byte, 102)
	for i := 0; i < 6; i++ {
		p[i] = 255
	}
	for i := 6; i < len(p); i += 6 {
		copy(p[i:], hw)
	}
	return p, nil
}
func Wake(ctx context.Context, d Device) error {
	// This is the UDP component, including when called by a combined wake.
	// Validate its required fields even if the caller supplied a VNC-only device.
	d.WakeMethod = "wol"
	if err := d.Validate(); err != nil {
		return err
	}
	packet, _ := MagicPacket(d.MAC)
	var local *net.UDPAddr
	if d.Interface != "" {
		iface, err := net.InterfaceByName(d.Interface)
		if err != nil {
			return fmt.Errorf("网卡: %w", err)
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, a := range addrs {
			ip, _, e := net.ParseCIDR(a.String())
			if e == nil && ip.To4() != nil {
				local = &net.UDPAddr{IP: ip.To4()}
				break
			}
		}
		if local == nil {
			return errors.New("指定网卡没有 IPv4 地址")
		}
	}
	conn, err := net.DialUDP("udp4", local, &net.UDPAddr{IP: net.ParseIP(d.Broadcast).To4(), Port: d.Port})
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err = conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	for i := 0; i < d.Repeat; i++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = conn.Write(packet); err != nil {
			return fmt.Errorf("发送唤醒包: %w", err)
		}
		if i+1 < d.Repeat {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}
func Probe(ctx context.Context, d Device) (bool, error) {
	if d.IP == "" || d.ProbePort == 0 {
		return false, errors.New("请先配置检测 IP 和 TCP 端口")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	c, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(d.IP, strconv.Itoa(d.ProbePort)))
	if err != nil {
		return false, nil
	}
	c.Close()
	return true, nil
}

type Network struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Broadcast string `json:"broadcast"`
}

func Networks() ([]Network, error) {
	result := []Network{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, e := iface.Addrs()
		if e != nil {
			continue
		}
		for _, a := range addrs {
			ip, n, e := net.ParseCIDR(a.String())
			if e != nil || ip.To4() == nil {
				continue
			}
			ip = ip.To4()
			if len(n.Mask) != 4 {
				continue
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^n.Mask[i]
			}
			result = append(result, Network{iface.Name, ip.String(), b.String()})
		}
	}
	return result, nil
}
