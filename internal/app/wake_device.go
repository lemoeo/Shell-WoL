package app

import (
	"context"
	"errors"
	"time"

	"github.com/lemoeo/Shell-WoL/internal/vncprobe"
)

type WakeResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	WOL     string `json:"wol,omitempty"`
	VNC     string `json:"vnc,omitempty"`
	Warning string `json:"warning,omitempty"`
}
type wakeExecutor struct {
	wol func(context.Context, Device) error
	vnc func(context.Context, vncprobe.Config, func(string)) error
}

func defaultWakeExecutor() wakeExecutor { return wakeExecutor{Wake, vncprobe.Run} }

func ExecuteWake(ctx context.Context, d Device, password []byte, progress func(string)) WakeResult {
	return defaultWakeExecutor().execute(ctx, d, password, progress)
}

func (e wakeExecutor) execute(parent context.Context, d Device, password []byte, progress func(string)) WakeResult {
	failure := func(err error) WakeResult { return WakeResult{Status: "failed", Message: err.Error()} }
	if err := d.Validate(); err != nil {
		return failure(err)
	}
	if d.UsesVNC() {
		if err := vncprobe.ValidatePassword(password); err != nil {
			return failure(err)
		}
	}
	if progress == nil {
		progress = func(string) {}
	}
	hold := time.Duration(d.VNC.HoldSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, 30*time.Second+hold)
	defer cancel()
	r := WakeResult{Status: "done"}
	var wolErr, vncErr error
	if d.WakeMethod != "mac_vnc" {
		progress("正在发送 WoL 魔术包")
		wolErr = e.wol(ctx, d)
		r.WOL = "魔术包已发送"
		if wolErr != nil {
			r.WOL = "WoL 发送失败：" + wolErr.Error()
		}
	}
	if d.UsesVNC() {
		// The 30-second pre-auth budget is shared by WoL/network readiness retries.
		authUntil := time.Now().Add(30 * time.Second)
		if end, _ := ctx.Deadline(); end.Add(-hold).Before(authUntil) {
			authUntil = end.Add(-hold)
		}
		for {
			remaining := time.Until(authUntil)
			if remaining <= 0 {
				vncErr = context.DeadlineExceeded
				break
			}
			c := vncprobe.Config{Host: d.IP, Port: d.VNC.Port, Stage: vncprobe.Auth, Username: d.VNC.Username, Password: password, Hold: hold, Timeout: remaining + hold, AuthTimeout: remaining}
			if d.WakeMethod == "wol_mac_vnc" {
				c.DialTimeout = min(3*time.Second, remaining)
			}
			c.Progress = func(phase vncprobe.Phase) {
				switch phase {
				case vncprobe.Connecting:
					progress("正在连接 Mac")
				case vncprobe.Authenticating:
					progress("正在验证 Mac 身份")
				case vncprobe.Holding:
					progress("认证成功，正在保持连接")
				}
			}
			vncErr = e.vnc(ctx, c, nil)
			var dialErr *vncprobe.ConnectError
			// Only failed TCP dials may be retried. Once connected, even an
			// ambiguous protocol failure never causes another credential attempt.
			if d.WakeMethod != "wol_mac_vnc" || !errors.As(vncErr, &dialErr) || ctx.Err() != nil || time.Until(authUntil) <= time.Second {
				break
			}
			progress("等待 Mac 网络恢复")
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				vncErr = ctx.Err()
			case <-timer.C:
			}
			if ctx.Err() != nil {
				break
			}
		}
		r.VNC = "VNC 认证成功，唤醒流程已完成"
		if vncErr != nil {
			r.VNC = "VNC 唤醒失败：" + vncErr.Error()
		}
	}
	if d.WakeMethod == "wol" {
		r.Message = r.WOL
		if wolErr != nil {
			r.Status = "failed"
		}
		return r
	}
	r.Message = r.VNC
	if d.WakeMethod == "wol_mac_vnc" {
		r.Message = r.WOL + "；" + r.VNC
	}
	if vncErr != nil {
		r.Status = "failed"
		if d.WakeMethod == "wol_mac_vnc" && wolErr == nil {
			r.Status = "partial"
		}
	}
	if wolErr != nil && vncErr == nil {
		r.Status = "partial"
	}
	return r
}
