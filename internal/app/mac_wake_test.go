package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemoeo/Shell-WoL/internal/vncprobe"
)

func macDevice(id string) Device {
	return Device{ID: id, Name: "Mac mini", WakeMethod: "mac_vnc", IP: "192.0.2.10", VNC: VNCConfig{Port: 5900, Username: "mac-user", HoldSeconds: 2}}
}

func TestEncryptedCredentialLifecycleAndWriteOnlyExport(t *testing.T) {
	s := newStore(t)
	d := macDevice("mac")
	secret := []byte("mac-password-fixture")
	if err := s.SaveDeviceCredentials(d, true, secret, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckData(s.dir, false, false); err != nil {
		t.Fatal("valid credentials failed check", err)
	}
	for _, name := range []string{"devices.yaml", "credentials.yaml"} {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, secret) {
			t.Fatal("plaintext credential on disk")
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"credentials.yaml", "credentials.key"} {
			i, _ := os.Stat(filepath.Join(s.dir, name))
			if i.Mode().Perm() != 0600 {
				t.Fatal("private file permissions", name, i.Mode())
			}
		}
	}
	dto, _ := json.Marshal(s.Devices())
	if bytes.Contains(dto, secret) || bytes.Contains(dto, []byte("vnc_password")) || !bytes.Contains(dto, []byte(`"password_configured":true`)) {
		t.Fatal(string(dto))
	}
	exported, err := s.Export()
	if err != nil || bytes.Contains(exported, secret) || bytes.Contains(exported, []byte("password")) {
		t.Fatal("credential in export", err)
	}
	if err = s.SaveDevice(d, false); err != nil {
		t.Fatal("blank password did not preserve existing", err)
	}
	_, p, err := s.WakeSnapshot(d.ID)
	if err != nil || !bytes.Equal(p, secret) {
		t.Fatal("decrypt", err)
	}
	clear(p)
	d.VNC.HoldSeconds = 0
	if err = s.SaveDevice(d, false); err != nil {
		t.Fatal("hold change invalidated credential", err)
	}
	d.IP = "192.0.2.11"
	if s.SaveDevice(d, false) == nil {
		t.Fatal("destination change silently reused password")
	}
	if err = s.SaveDeviceCredentials(d, false, []byte("replacement-password"), false); err != nil {
		t.Fatal(err)
	}
	old := d
	old.IP = "192.0.2.10"
	if _, err = readDevicePassword(s.dir, old); err == nil {
		t.Fatal("old destination could decrypt new password")
	}
	if err = s.SaveDeviceCredentials(d, false, nil, true); err != nil {
		t.Fatal(err)
	}
	if s.Devices()[0].PasswordConfigured {
		t.Fatal("password flag survived clear")
	}
	if _, _, err = s.WakeSnapshot(d.ID); err == nil {
		t.Fatal("cleared credential can wake")
	}
	if err = s.SaveDeviceCredentials(d, false, secret, false); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	c, err := readCredentials(s.dir)
	if err != nil || len(c.Credentials) != 0 {
		t.Fatal("orphan credential", err)
	}
}

func TestCredentialPersistenceMissingKeyAndTampering(t *testing.T) {
	s := newStore(t)
	d := macDevice("mac")
	if err := s.SaveDeviceCredentials(d, true, []byte("fixture-secret"), false); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := ReadWakeDefaults(s.dir, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadDevicePassword(s.dir, snapshot)
	if err != nil || string(p) != "fixture-secret" {
		t.Fatal("CLI snapshot", err)
	}
	clear(p)
	keyPath := filepath.Join(s.dir, "credentials.key")
	key, _ := os.ReadFile(keyPath)
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.WakeSnapshot(d.ID); err == nil {
		t.Fatal("missing key accepted")
	}
	if err = s.SaveDeviceCredentials(d, false, []byte("new-password"), false); err == nil {
		t.Fatal("missing key was silently replaced")
	}
	if _, err = os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new key created over lost key")
	}
	if err = os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	c, _ := readCredentials(s.dir)
	r := c.Credentials[d.ID]
	r.Data = strings.Repeat("A", 80)
	c.Credentials[d.ID] = r
	if err = saveCredentials(s.dir, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.WakeSnapshot(d.ID); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestCredentialSaveFailureRestoresState(t *testing.T) {
	s := newStore(t)
	d := macDevice("mac")
	if err := s.SaveDeviceCredentials(d, true, []byte("old-password"), false); err != nil {
		t.Fatal(err)
	}
	// Make only the device-file commit fail after writing replacement ciphertext.
	path := filepath.Join(s.dir, "devices.yaml")
	before, _ := os.ReadFile(path)
	os.Remove(path)
	os.Mkdir(path, 0700)
	err := s.SaveDeviceCredentials(d, false, []byte("new-password"), false)
	if err == nil {
		t.Fatal("failed commit reported success")
	}
	p, err := readDevicePassword(s.dir, d)
	if err != nil || string(p) != "old-password" {
		t.Fatal("old secret not restored", err)
	}
	clear(p)
	os.Remove(path)
	os.WriteFile(path, before, 0600)
}

func TestWakeRoutingHoldAndPartialResults(t *testing.T) {
	for _, method := range []string{"wol", "mac_vnc", "wol_mac_vnc"} {
		for _, wolFails := range []bool{false, true} {
			for _, vncFails := range []bool{false, true} {
				d := testDevice("mac")
				d.WakeMethod = method
				d.IP = "192.0.2.10"
				d.VNC = VNCConfig{Port: 5900, Username: "user", HoldSeconds: 2}
				wolCalls, vncCalls := 0, 0
				e := wakeExecutor{
					wol: func(context.Context, Device) error {
						wolCalls++
						if wolFails {
							return errors.New("fixture UDP error")
						}
						return nil
					},
					vnc: func(ctx context.Context, c vncprobe.Config, _ func(string)) error {
						vncCalls++
						if c.Stage != vncprobe.Auth || c.Hold != 2*time.Second || c.AuthTimeout > 30*time.Second {
							t.Fatal("wrong production VNC phase", c.Stage, c.Hold, c.AuthTimeout)
						}
						if vncFails {
							return vncprobe.ErrAuthRejected
						}
						return nil
					},
				}
				r := e.execute(context.Background(), d, []byte("password"), nil)
				if method == "mac_vnc" && wolCalls != 0 || method != "mac_vnc" && wolCalls != 1 || method == "wol" && vncCalls != 0 || method != "wol" && vncCalls != 1 {
					t.Fatal("wrong route", method, wolCalls, vncCalls)
				}
				want := "done"
				if method == "wol" && wolFails || method == "mac_vnc" && vncFails || method == "wol_mac_vnc" && wolFails && vncFails {
					want = "failed"
				}
				if method == "wol_mac_vnc" && wolFails != vncFails {
					want = "partial"
				}
				if r.Status != want {
					t.Fatal(method, wolFails, vncFails, r)
				}
			}
		}
	}
}

func TestOnlyPreConnectionFailureCanRetry(t *testing.T) {
	d := testDevice("mac")
	d.WakeMethod = "wol_mac_vnc"
	d.IP = "192.0.2.1"
	d.VNC = VNCConfig{Username: "user", Port: 5900}
	calls := 0
	e := wakeExecutor{wol: func(context.Context, Device) error { return nil }, vnc: func(context.Context, vncprobe.Config, func(string)) error {
		calls++
		if calls == 1 {
			return &vncprobe.ConnectError{Err: errors.New("not ready")}
		}
		return nil
	}}
	if r := e.execute(context.Background(), d, []byte("password"), nil); r.Status != "done" || calls != 2 {
		t.Fatal(r, calls)
	}
	for _, failure := range []error{vncprobe.ErrAuthRejected, errors.New("authentication response lost"), vncprobe.ErrAuthLimited} {
		calls = 0
		e.vnc = func(context.Context, vncprobe.Config, func(string)) error { calls++; return failure }
		if r := e.execute(context.Background(), d, []byte("password"), nil); calls != 1 || r.Status != "partial" {
			t.Fatal("retried credentials", r, calls)
		}
	}
}

func TestWakeJobsDeduplicateBoundAndCancel(t *testing.T) {
	s := newStore(t)
	j := newWakeJobs()
	defer j.close()
	for _, id := range []string{"one", "two", "three"} {
		if err := s.SaveDeviceCredentials(macDevice(id), true, []byte("password"), false); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	j.run = func(ctx context.Context, _ Device, p []byte, progress func(string)) WakeResult {
		calls.Add(1)
		started <- struct{}{}
		progress("正在测试")
		<-ctx.Done()
		return WakeResult{Status: "failed", Message: "已取消"}
	}
	one, err := j.submit(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := j.submit(s, "one")
	if err != nil || again.ID != one.ID {
		t.Fatal("duplicate job", err)
	}
	if _, err = j.submit(s, "two"); err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	if _, err = j.submit(s, "three"); !errors.Is(err, errJobsBusy) {
		t.Fatal("unbounded jobs", err)
	}
	if err = j.withIdleDevice("one", func() error { t.Fatal("mutation during job"); return nil }); err == nil {
		t.Fatal("edit allowed during job")
	}
	j.close()
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	job, ok := j.get(one.ID)
	if !ok || job.State != "failed" {
		t.Fatal(job)
	}
	if _, err = j.submit(s, "three"); !errors.Is(err, errJobsBusy) {
		t.Fatal("closed manager accepted job")
	}
}

func authenticatedMacClient(t *testing.T) (*Server, *testClient) {
	t.Helper()
	s := NewServer(authStore(t))
	t.Cleanup(s.Close)
	c := &testClient{h: s.Handler()}
	w := c.call("POST", "/api/login", map[string]string{"password": "test-password-123"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	c.cookie = w.Result().Cookies()[0]
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	c.csrf = result["csrf"]
	return s, c
}

func TestMacAPIWriteOnlyCredentialsAndProtectedJobs(t *testing.T) {
	s, c := authenticatedMacClient(t)
	d := macDevice("mac")
	payload := map[string]any{"id": d.ID, "name": d.Name, "wake_method": d.WakeMethod, "ip": d.IP, "vnc": d.VNC, "vnc_password": "private-fixture-password"}
	w := c.call("POST", "/api/devices", payload)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/devices", "/api/export"} {
		w = c.call("GET", path, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-fixture-password") || strings.Contains(w.Body.String(), "vnc_password") {
			t.Fatal("credential leak", w.Body.String())
		}
	}
	delete(payload, "vnc_password")
	w = c.call("PUT", "/api/devices/mac", payload)
	if w.Code != 200 {
		t.Fatal("blank edit", w.Body.String())
	}
	finish := make(chan struct{})
	started := make(chan struct{})
	s.jobs.run = func(ctx context.Context, _ Device, _ []byte, progress func(string)) WakeResult {
		close(started)
		progress("认证成功，正在保持连接")
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return WakeResult{Status: "done", Message: "VNC 认证成功"}
	}
	w = c.call("POST", "/api/devices/mac/wake", nil)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var accepted map[string]string
	json.Unmarshal(w.Body.Bytes(), &accepted)
	id := accepted["job_id"]
	<-started
	w = c.call("POST", "/api/devices/mac/wake", nil)
	if !strings.Contains(w.Body.String(), id) {
		t.Fatal("HTTP duplicate job")
	}
	if w = c.call("DELETE", "/api/devices/mac", nil); w.Code != 409 {
		t.Fatal("delete while running", w.Code)
	}
	unauth := testClient{h: s.Handler()}
	if w = unauth.call("GET", "/api/wake-jobs/"+id, nil); w.Code != 401 {
		t.Fatal("job exposed")
	}
	old := c.csrf
	c.csrf = ""
	if w = c.call("POST", "/api/devices/mac/wake", nil); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	c.csrf = old
	close(finish)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w = c.call("GET", "/api/wake-jobs/"+id, nil)
		var job WakeJob
		json.Unmarshal(w.Body.Bytes(), &job)
		if job.State == "done" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(w.Body.String(), `"state":"done"`) {
		t.Fatal(w.Body.String())
	}
	payload["clear_vnc_password"] = true
	w = c.call("PUT", "/api/devices/mac", payload)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = c.call("POST", "/api/devices/mac/wake", nil)
	if w.Code != 400 {
		t.Fatal("wake without credential", w.Code)
	}
}

func TestMacWakeConfigurationDefaultsAndBounds(t *testing.T) {
	d := macDevice("mac")
	d.VNC.Port = 0
	d.VNC.HoldSeconds = 0
	if err := d.Validate(); err != nil || d.VNC.Port != 5900 {
		t.Fatal("Mac defaults", err, d.VNC)
	}
	for _, hold := range []int{-1, 31} {
		x := d
		x.VNC.HoldSeconds = hold
		if x.Validate() == nil {
			t.Fatal("hold accepted", hold)
		}
	}
	x := d
	x.WakeMethod = "wol_mac_vnc"
	if x.Validate() == nil {
		t.Fatal("combined mode accepted missing MAC")
	}
	x = d
	x.IP = ""
	if x.Validate() == nil {
		t.Fatal("Mac accepted missing IP")
	}
	x = d
	x.WakeMethod = "unknown"
	if x.Validate() == nil {
		t.Fatal("unknown wake method")
	}
	old := testDevice("legacy")
	if err := old.Validate(); err != nil || old.WakeMethod != "wol" {
		t.Fatal("legacy default", err)
	}
}

func TestRawWoLStillRequiresMACAndUDPParameters(t *testing.T) {
	d := macDevice("mac")
	if err := Wake(context.Background(), d); err == nil {
		t.Fatal("raw UDP primitive accepted a VNC-only device without MAC")
	}
}
