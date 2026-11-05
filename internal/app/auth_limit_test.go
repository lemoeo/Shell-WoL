package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func authRequest(h http.Handler, path, peer, forwarded string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	r.RemoteAddr = peer
	r.Header.Set("Content-Type", "application/json")
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func authStore(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	// Use the real bcrypt path at lower cost to keep repeated failure tests fast.
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	c.PasswordHash = string(hash)
	if err = s.saveConfig(c); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoginLimitsArePerIPAndPreserveSessions(t *testing.T) {
	s := NewServer(authStore(t))
	h := s.Handler()
	wrong := map[string]string{"password": "wrong"}
	correct := map[string]string{"password": "test-password-123"}
	for i := 0; i < 10; i++ {
		w := authRequest(h, "/api/login", "192.0.2.1:1000", "", wrong)
		if w.Code != 401 {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := authRequest(h, "/api/login", "192.0.2.1:2000", "", correct)
	if w.Code != 429 {
		t.Fatalf("blocked source: %d", w.Code)
	}
	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || seconds < 1 || seconds > 60 {
		t.Fatalf("invalid Retry-After: %q", w.Header().Get("Retry-After"))
	}
	w = authRequest(h, "/api/login", "192.0.2.2:1000", "", correct)
	if w.Code != 200 {
		t.Fatalf("another IP cannot log in: %d %s", w.Code, w.Body.String())
	}
	// A rate-limited source can still use an existing authenticated session.
	r := httptest.NewRequest("GET", "/api/devices", nil)
	r.RemoteAddr = "192.0.2.1:2000"
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login limit affected existing session: %d", w.Code)
	}
}

func TestClosedSetupDoesNotConsumeAuthQuota(t *testing.T) {
	s := NewServer(authStore(t))
	h := s.Handler()
	for i := 0; i < 100; i++ {
		w := authRequest(h, "/api/setup", "192.0.2.1:1000", "", nil)
		if w.Code != 409 {
			t.Fatalf("closed setup: %d", w.Code)
		}
	}
	if len(s.setupLimit.windows) != 0 || len(s.loginLimit.windows) != 0 {
		t.Fatal("closed setup consumed quota")
	}
	w := authRequest(h, "/api/login", "192.0.2.1:1000", "", map[string]string{"password": "test-password-123"})
	if w.Code != 200 {
		t.Fatalf("closed setup blocked login: %d", w.Code)
	}
}

func TestSetupHasSeparatePerIPLimit(t *testing.T) {
	s := NewServer(newStore(t))
	h := s.Handler()
	for i := 0; i < 5; i++ {
		w := authRequest(h, "/api/setup", "192.0.2.1:1000", "", map[string]string{"token": "wrong"})
		if w.Code != 403 {
			t.Fatalf("setup attempt %d: %d", i, w.Code)
		}
	}
	w := authRequest(h, "/api/setup", "192.0.2.1:1001", "", map[string]string{"token": s.SetupToken()})
	if w.Code != 429 {
		t.Fatalf("setup limit missing: %d", w.Code)
	}
	for i := 0; i < 10; i++ {
		w = authRequest(h, "/api/login", "192.0.2.2:1000", "", map[string]string{"password": "wrong"})
		if w.Code != 409 {
			t.Fatalf("uninitialized login attempt %d: %d", i, w.Code)
		}
	}
	w = authRequest(h, "/api/login", "192.0.2.2:1000", "", map[string]string{})
	if w.Code != 429 {
		t.Fatal("login limit missing")
	}
	// Source B's exhausted login budget and source A's setup budget cannot
	// prevent source B from initializing with the correct token.
	w = authRequest(h, "/api/setup", "192.0.2.2:1000", "", map[string]string{"token": s.SetupToken(), "password": "test-password-123"})
	if w.Code != 200 {
		t.Fatalf("independent setup blocked: %d %s", w.Code, w.Body.String())
	}
	w = authRequest(h, "/api/setup", "192.0.2.1:1000", "", nil)
	if w.Code != 409 {
		t.Fatal("closed setup must bypass even an exhausted setup limit")
	}
}

func TestSourceLimiterExpiryCapacityAndConcurrency(t *testing.T) {
	now := time.Now()
	l := newSourceLimiter(10)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.retryAfter("one", now) == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("concurrent admissions: %d", admitted.Load())
	}
	if delay := l.retryAfter("one", now.Add(59*time.Second)); delay != time.Second {
		t.Fatalf("remaining window incorrect: %s", delay)
	}
	if delay := l.retryAfter("one", now.Add(time.Minute)); delay != 0 {
		t.Fatalf("rejected attempts extended expiry: %s", delay)
	}
	for i := 0; i < maxAuthSources; i++ {
		l.retryAfter(strconv.Itoa(i), now.Add(2*time.Minute))
	}
	if len(l.windows) != maxAuthSources {
		t.Fatalf("expired sources not reclaimed: %d", len(l.windows))
	}
	if delay := l.retryAfter("overflow", now.Add(2*time.Minute+30*time.Second)); delay != 30*time.Second {
		t.Fatalf("full table should wait for earliest expiry: %s", delay)
	}
	if len(l.windows) != maxAuthSources {
		t.Fatal("source table exceeded capacity")
	}
	if l.retryAfter("0", now.Add(2*time.Minute+30*time.Second)) != 0 {
		t.Fatal("capacity rejected an existing source with budget")
	}
	if l.retryAfter("overflow", now.Add(3*time.Minute)) != 0 || len(l.windows) != 1 {
		t.Fatal("expired table did not recover")
	}
}

func TestSourceLimiterReclaimsBetweenCleanupsWithoutEviction(t *testing.T) {
	l := newSourceLimiter(1)
	now := time.Now()
	l.retryAfter("first", now)
	for i := 1; i < maxAuthSources; i++ {
		l.retryAfter(strconv.Itoa(i), now.Add(30*time.Second))
	}
	l.cleanup = now.Add(2 * time.Minute)
	if l.retryAfter("new", now.Add(time.Minute)) != 0 || len(l.windows) != maxAuthSources {
		t.Fatal("failed to reclaim an expired slot before periodic cleanup")
	}
	if l.retryAfter("1", now.Add(time.Minute)) != 30*time.Second {
		t.Fatal("active blocked source was evicted")
	}
}

func TestTrustedProxyClientIP(t *testing.T) {
	s := NewServer(newStore(t))
	proxies, err := parseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8", "::1", "2001:db8:1::/48", "::ffff:192.168.0.0/112"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer, forwarded, want string
		trust                       bool
	}{
		{"default ignores forwarding", "127.0.0.1:1234", "192.0.2.1", "127.0.0.1", false},
		{"untrusted peer", "192.0.2.1:1234", "192.0.2.2", "192.0.2.1", true},
		{"trusted peer", "127.0.0.1:1234", "192.0.2.1", "192.0.2.1", true},
		{"trusted chain", "127.0.0.1:1234", "192.0.2.1, 10.0.0.1", "192.0.2.1", true},
		{"spoofed leftmost", "127.0.0.1:1234", "198.51.100.2, 192.0.2.1", "192.0.2.1", true},
		{"IPv6", "[::1]:1234", "2001:db8:2::1, 2001:db8:1::1", "2001:db8:2::1", true},
		{"mapped peer", "[::ffff:192.0.2.1]:1234", "192.0.2.2", "192.0.2.1", true},
		{"mapped forwarded", "[::1]:1234", "::ffff:192.0.2.1", "192.0.2.1", true},
		{"mapped trusted prefix", "192.168.1.1:1234", "192.0.2.1", "192.0.2.1", true},
		{"IPv6 zone peer", "[fe80::1%eth0]:1234", "", "fe80::1", false},
		{"invalid peer", "invalid", "192.0.2.1", "unknown", true},
		{"empty header", "127.0.0.1:1234", "", "127.0.0.1", true},
		{"invalid header", "127.0.0.1:1234", "bad, 192.0.2.1", "127.0.0.1", true},
		{"empty hop", "127.0.0.1:1234", "192.0.2.1,", "127.0.0.1", true},
		{"forwarded port", "127.0.0.1:1234", "192.0.2.1:1234", "127.0.0.1", true},
		{"forwarded zone", "127.0.0.1:1234", "fe80::1%eth0", "127.0.0.1", true},
		{"too many hops", "127.0.0.1:1234", strings.Repeat("192.0.2.1,", 32) + "192.0.2.1", "127.0.0.1", true},
		{"oversized header", "127.0.0.1:1234", strings.Repeat(" ", 4097), "127.0.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.trustedProxies = nil
			if tc.trust {
				s.trustedProxies = proxies
			}
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("X-Real-IP", "203.0.113.1")
			r.Header.Set("Forwarded", "for=203.0.113.1")
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	s.trustedProxies = proxies
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "192.0.2.1")
	r.Header.Add("X-Forwarded-For", "10.0.0.1")
	if got := s.clientIP(r); got != "192.0.2.1" {
		t.Fatalf("multiple header lines: %s", got)
	}
}

func TestForwardedLoginLimits(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(fmt.Sprint("trusted=", trust), func(t *testing.T) {
			store := authStore(t)
			if trust {
				c := store.Config()
				c.TrustedProxies = []string{"127.0.0.1"}
				if err := store.saveConfig(c); err != nil {
					t.Fatal(err)
				}
			}
			s := NewServer(store)
			h := s.Handler()
			for i := 0; i < 10; i++ {
				if w := authRequest(h, "/api/login", "127.0.0.1:1000", "192.0.2.1", map[string]string{"password": "wrong"}); w.Code != 401 {
					t.Fatalf("login: %d", w.Code)
				}
			}
			w := authRequest(h, "/api/login", "127.0.0.1:2000", "192.0.2.2", map[string]string{"password": "test-password-123"})
			want := 429
			if trust {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("changed forwarded IP: got %d, want %d", w.Code, want)
			}
		})
	}
}

func TestBusyPasswordWorkAndSetupDoNotQueue(t *testing.T) {
	s := NewServer(authStore(t))
	started, release := make(chan struct{}, 2), make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, admitted := s.runPasswordWork(httptest.NewRecorder(), func() error {
				started <- struct{}{}
				<-release
				return nil
			})
			if !admitted {
				t.Error("free password slot refused")
			}
		}()
	}
	<-started
	<-started
	// Every excess request is answered immediately while both slots are held.
	w := authRequest(s.Handler(), "/api/login", "192.0.2.1:1000", "", map[string]string{"password": "test-password-123"})
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Errorf("busy login: %d %s", w.Code, w.Body.String())
	}
	setup := NewServer(newStore(t))
	setup.passwordWork = s.passwordWork
	input := map[string]string{"token": setup.SetupToken(), "password": "test-password-123"}
	w = authRequest(setup.Handler(), "/api/setup", "192.0.2.1:1000", "", input)
	if w.Code != 503 || setup.store.Config().PasswordHash != "" {
		t.Error("busy setup was queued or changed password")
	}
	close(release)
	wg.Wait()
	w = authRequest(s.Handler(), "/api/login", "192.0.2.1:1000", "", map[string]string{"password": "test-password-123"})
	if w.Code != 200 {
		t.Errorf("slots not released: %d", w.Code)
	}
	setup.setupMu.Lock()
	w = authRequest(setup.Handler(), "/api/setup", "192.0.2.1:1000", "", input)
	setup.setupMu.Unlock()
	if w.Code != 503 {
		t.Errorf("concurrent setup queued: %d", w.Code)
	}
	w = authRequest(setup.Handler(), "/api/setup", "192.0.2.1:1000", "", input)
	if w.Code != 200 {
		t.Errorf("setup did not recover: %d %s", w.Code, w.Body.String())
	}
}

func TestProxyTrustIsYAMLOnlyAndPreservedByWebConfig(t *testing.T) {
	store := authStore(t)
	c := store.Config()
	c.TrustedProxies = []string{"127.0.0.1/32", "::1/128"}
	if err := store.saveConfig(c); err != nil {
		t.Fatal(err)
	}
	copy := store.Config()
	copy.TrustedProxies[0] = "0.0.0.0/0"
	if store.Config().TrustedProxies[0] != "127.0.0.1/32" {
		t.Fatal("Config exposes mutable proxy trust")
	}
	client := testClient{h: NewServer(store).Handler()}
	w := client.call("POST", "/api/login", map[string]string{"password": "test-password-123"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	client.cookie = w.Result().Cookies()[0]
	var login map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	client.csrf = login["csrf"]
	w = client.call("GET", "/api/config", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "trusted_proxies") {
		t.Fatal("YAML-only setting exposed in config API")
	}
	c = DefaultConfig()
	c.LogLimit = 50
	w = client.call("PUT", "/api/config", c)
	if w.Code != 200 || len(store.Config().TrustedProxies) != 2 || store.Config().LogLimit != 50 {
		t.Fatalf("web save cleared proxy trust: %d %s", w.Code, w.Body.String())
	}
	w = client.call("PUT", "/api/config", map[string]any{"trusted_proxies": []string{"0.0.0.0/0"}})
	if w.Code != 400 {
		t.Fatal("API can change proxy trust")
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "127.0.0.1/32") {
		t.Fatal("proxy trust missing from saved YAML")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Config().TrustedProxies; len(got) != 2 || got[0] != "127.0.0.1/32" {
		t.Fatalf("YAML trust lost on restart: %v", got)
	}
}

func TestInvalidProxyTrustRejectedAtStartup(t *testing.T) {
	for _, entry := range []string{"not-an-ip", "192.0.2.1:8080", "192.0.2.1/33", "fe80::1%eth0", "::ffff:192.0.2.0/80", ""} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			data := fmt.Sprintf("trusted_proxies:\n  - %q\n", entry)
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := OpenStore(dir); err == nil {
				s.Close()
				t.Fatal("invalid proxy trust accepted")
			}
		})
	}
	c := DefaultConfig()
	c.TrustedProxies = make([]string, 65)
	for i := range c.TrustedProxies {
		c.TrustedProxies[i] = "127.0.0.1"
	}
	if c.Validate() == nil {
		t.Fatal("unbounded proxy list accepted")
	}
}
