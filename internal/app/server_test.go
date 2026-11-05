package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testClient struct {
	h      http.Handler
	cookie *http.Cookie
	csrf   string
}

func (c *testClient) call(method, path string, body any) *httptest.ResponseRecorder {
	var b io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		b = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, b)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	return w
}
func TestSetupLoginCRUDAndCSRF(t *testing.T) {
	store := newStore(t)
	server := NewServer(store)
	c := testClient{h: server.Handler()}
	if w := c.call("GET", "/api/devices", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := c.call("POST", "/api/setup", map[string]string{"token": "wrong", "password": "a-secure-password"}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := c.call("POST", "/api/setup", map[string]string{"token": server.SetupToken(), "password": "a-secure-password"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := c.call("POST", "/api/setup", nil); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := c.call("POST", "/api/login", map[string]string{"password": "wrong"}); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := c.call("POST", "/api/login", map[string]string{"password": "a-secure-password"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	c.cookie = cookies[0]
	var login map[string]string
	json.Unmarshal(w.Body.Bytes(), &login)
	if w = c.call("POST", "/api/devices", testDevice("pc")); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	c.csrf = login["csrf"]
	if w = c.call("POST", "/api/devices", testDevice("pc")); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	d := testDevice("pc")
	d.Note = "edited"
	if w = c.call("PUT", "/api/devices/pc", d); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = c.call("GET", "/api/devices", nil); !strings.Contains(w.Body.String(), "edited") {
		t.Fatal("update absent")
	}
	if w = c.call("GET", "/api/config", nil); strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("password hash exposed")
	}
	if w = c.call("GET", "/api/export", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "devices:") {
		t.Fatal("export failed")
	}
	if w = c.call("POST", "/api/devices/pc/probe", nil); w.Code != 400 {
		t.Fatal("unconfigured probe accepted")
	}
	if w = c.call("DELETE", "/api/devices/pc", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = c.call("POST", "/api/logout", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = c.call("GET", "/api/devices", nil); w.Code != 401 {
		t.Fatal("session survived logout")
	}
}
func TestSecurityBoundaries(t *testing.T) {
	s := NewServer(newStore(t))
	h := s.Handler()
	r := httptest.NewRequest("POST", "http://localhost/api/login", strings.NewReader(`{"password":"test"}`))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	r = httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{}`))
	r.RemoteAddr = "192.168.1.50:1234"
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("setup without a token accepted")
	}
	c := testClient{h: h}
	for i := 0; i < 10; i++ {
		c.call("POST", "/api/login", map[string]string{"password": "wrong"})
	}
	if w = c.call("POST", "/api/login", map[string]string{}); w.Code != 429 {
		t.Fatal("rate limit missing")
	}
	for _, addr := range []string{"127.0.0.1:8080", "0.0.0.0:8080", ":8080", "localhost:8080", "[::]:8080"} {
		if err := ValidateListen(addr); err != nil {
			t.Fatalf("valid listen %s: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1", "0.0.0.0:", "0.0.0.0:-1", "0.0.0.0:65536", "0.0.0.0:http"} {
		if ValidateListen(addr) == nil {
			t.Fatalf("invalid listen accepted: %s", addr)
		}
	}
	w = c.call("GET", "/", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Shell-WoL") {
		t.Fatal("embedded UI unavailable")
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("CSP missing")
	}
}

func TestExplicitHTTPSOrigin(t *testing.T) {
	store := newStore(t)
	config := store.Config()
	config.PublicURL = "https://wol.example.com"
	if e := store.SaveConfig(config); e != nil {
		t.Fatal(e)
	}
	if e := store.SetPassword("test-https-password"); e != nil {
		t.Fatal(e)
	}
	h := NewServer(store).Handler()
	request := httptest.NewRequest("POST", "http://wol.example.com/api/login", strings.NewReader(`{"password":"test-https-password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://wol.example.com")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if !response.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS public origin needs Secure cookie")
	}
	request = httptest.NewRequest("POST", "http://wol.example.com/api/login", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://wol.example.com")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("wrong scheme accepted")
	}
}

func TestLANBrowserInitialization(t *testing.T) {
	store := newStore(t)
	server := NewServer(store)
	handler := server.Handler()
	request := func(method, path string, body any, origin string) *httptest.ResponseRecorder {
		var raw bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&raw).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://router.lan:8080"+path, &raw)
		r.RemoteAddr = "192.168.1.50:12345"
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/api/session", nil, "")
	if !strings.Contains(w.Body.String(), `"setup_required":true`) || strings.Contains(w.Body.String(), server.SetupToken()) {
		t.Fatal("invalid setup state or token exposed")
	}
	for _, path := range []string{"/api/devices", "/api/config", "/api/events"} {
		if request("GET", path, nil, "").Code != 401 {
			t.Fatal("management accessible before login")
		}
	}
	for _, token := range []string{"", "wrong"} {
		if request("POST", "/api/setup", map[string]string{"token": token, "password": "lan-browser-password"}, "http://router.lan:8080").Code != 403 {
			t.Fatal("invalid initialization token accepted")
		}
	}
	input := map[string]string{"token": server.SetupToken(), "password": "lan-browser-password"}
	if request("POST", "/api/setup", input, "http://evil.example").Code != 403 {
		t.Fatal("cross-origin initialization accepted")
	}
	w = request("POST", "/api/setup", input, "http://router.lan:8080")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	hash := store.Config().PasswordHash
	input["password"] = "replacement-password"
	if request("POST", "/api/setup", input, "http://router.lan:8080").Code != 409 || store.Config().PasswordHash != hash {
		t.Fatal("administrator reset through setup")
	}
	if request("GET", "/api/devices", nil, "").Code != 401 {
		t.Fatal("initialization bypassed login")
	}
	w = request("POST", "/api/login", map[string]string{"password": "lan-browser-password"}, "http://router.lan:8080")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("initialized password does not work")
	}
}
