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
		t.Fatal("remote setup accepted")
	}
	c := testClient{h: h}
	for i := 0; i < 10; i++ {
		c.call("POST", "/api/login", map[string]string{"password": "wrong"})
	}
	if w = c.call("POST", "/api/login", map[string]string{}); w.Code != 429 {
		t.Fatal("rate limit missing")
	}
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "localhost:8080"} {
		if ValidateListen(addr, false) == nil {
			t.Fatal("unauthenticated public listen accepted")
		}
	}
	if e := ValidateListen("127.0.0.1:8080", false); e != nil {
		t.Fatal(e)
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
