package app

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/*
var web embed.FS

type session struct {
	csrf    string
	expires time.Time
}
type Server struct {
	store                *Store
	setupToken           string
	mu                   sync.Mutex
	sessions             map[string]session
	authStart, wakeStart time.Time
	authCount, wakeCount int
}

func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func NewServer(s *Store) *Server {
	return &Server{store: s, setupToken: randomToken(), sessions: map[string]session{}}
}
func (s *Server) SetupToken() string { return s.setupToken }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.sessionInfo)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.protect(s.logout))
	mux.HandleFunc("GET /api/devices", s.protect(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.store.Devices()) }))
	mux.HandleFunc("POST /api/devices", s.protect(s.createDevice))
	mux.HandleFunc("PUT /api/devices/{id}", s.protect(s.updateDevice))
	mux.HandleFunc("DELETE /api/devices/{id}", s.protect(s.deleteDevice))
	mux.HandleFunc("POST /api/devices/{id}/wake", s.protect(s.wake))
	mux.HandleFunc("POST /api/devices/{id}/probe", s.protect(s.probe))
	mux.HandleFunc("GET /api/events", s.protect(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.store.Events()) }))
	mux.HandleFunc("GET /api/networks", s.protect(func(w http.ResponseWriter, r *http.Request) {
		v, e := Networks()
		if e != nil {
			apiError(w, 500, e)
			return
		}
		respond(w, 200, v)
	}))
	mux.HandleFunc("GET /api/config", s.protect(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, s.store.Config()) }))
	mux.HandleFunc("PUT /api/config", s.protect(func(w http.ResponseWriter, r *http.Request) {
		var c Config
		if !decode(w, r, &c) {
			return
		}
		if e := s.store.SaveConfig(c); e != nil {
			apiError(w, 400, e)
			return
		}
		respond(w, 200, map[string]string{"message": "已保存；监听地址在重启后生效"})
	}))
	mux.HandleFunc("GET /api/export", s.protect(func(w http.ResponseWriter, r *http.Request) {
		b, e := s.store.Export()
		if e != nil {
			apiError(w, 500, e)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="devices.yaml"`)
		w.Write(b)
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, 404, errors.New("接口不存在")) })
	assets, _ := fs.Sub(web, "web")
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				apiError(w, 403, errors.New("拒绝跨站请求"))
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				expected := ""
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				expected = scheme + "://" + r.Host
				if public := s.store.Config().PublicURL; public != "" {
					expected = strings.TrimSuffix(public, "/")
				}
				if origin != expected {
					apiError(w, 403, errors.New("拒绝跨源请求"))
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, e error) {
	respond(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		apiError(w, 415, errors.New("需要 application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		apiError(w, 400, e)
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		apiError(w, 400, errors.New("请求必须只含一个 JSON 对象"))
		return false
	}
	return true
}
func (s *Server) getSession(r *http.Request) (session, bool) {
	c, e := r.Cookie("wol_session")
	if e != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[c.Value]
	if !ok || time.Now().After(v.expires) {
		delete(s.sessions, c.Value)
		return session{}, false
	}
	return v, true
}
func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := s.getSession(r)
		if !ok {
			apiError(w, 401, errors.New("请先登录"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(v.csrf)) != 1 {
			apiError(w, 403, errors.New("无效的 CSRF 令牌"))
			return
		}
		next(w, r)
	}
}
func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.getSession(r)
	respond(w, 200, map[string]any{"authenticated": ok, "setup_required": s.store.Config().PasswordHash == "", "csrf": v.csrf})
}
func (s *Server) allow(auth bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	start, count, limit := &s.wakeStart, &s.wakeCount, 60
	if auth {
		start, count, limit = &s.authStart, &s.authCount, 10
	}
	if time.Since(*start) >= time.Minute {
		*start = time.Now()
		*count = 0
	}
	if *count >= limit {
		return false
	}
	*count++
	return true
}
func (s *Server) rate(w http.ResponseWriter, auth bool) bool {
	if s.allow(auth) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	apiError(w, 429, errors.New("请求过于频繁，请稍后重试"))
	return false
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.rate(w, true) {
		return
	}
	if s.store.Config().PasswordHash != "" {
		apiError(w, 409, errors.New("管理员已初始化"))
		return
	}
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(input.Token), []byte(s.setupToken)) != 1 {
		apiError(w, 403, errors.New("初始化令牌错误"))
		return
	}
	// Serialize setup attempts so two first-use requests cannot replace each other.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store.Config().PasswordHash != "" {
		apiError(w, 409, errors.New("管理员已初始化"))
		return
	}
	if e := s.store.SetPassword(input.Password); e != nil {
		apiError(w, 400, e)
		return
	}
	respond(w, 200, map[string]string{"message": "密码已设置，请登录"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.rate(w, true) {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	hash := s.store.Config().PasswordHash
	if hash == "" {
		apiError(w, 409, errors.New("请先初始化管理员"))
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		apiError(w, 401, errors.New("密码错误"))
		return
	}
	id, csrf := randomToken(), randomToken()
	s.mu.Lock()
	for k, v := range s.sessions {
		if time.Now().After(v.expires) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 100 {
		s.mu.Unlock()
		apiError(w, 429, errors.New("会话数量已达上限"))
		return
	}
	if old, e := r.Cookie("wol_session"); e == nil {
		delete(s.sessions, old.Value)
	}
	s.sessions[id] = session{csrf, time.Now().Add(12 * time.Hour)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wol_session", Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(s.store.Config().PublicURL, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]string{"csrf": csrf})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("wol_session")
	s.mu.Lock()
	delete(s.sessions, c.Value)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wol_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(s.store.Config().PublicURL, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var d Device
	if !decode(w, r, &d) {
		return
	}
	if d.ID == "" {
		d.ID = randomToken()[:16]
	}
	if e := s.store.SaveDevice(d, true); e != nil {
		apiError(w, 400, e)
		return
	}
	respond(w, 201, d)
}
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var d Device
	if !decode(w, r, &d) {
		return
	}
	if d.ID != "" && d.ID != r.PathValue("id") {
		apiError(w, 400, errors.New("设备 ID 不可修改"))
		return
	}
	d.ID = r.PathValue("id")
	if e := s.store.SaveDevice(d, false); e != nil {
		apiError(w, 400, e)
		return
	}
	respond(w, 200, d)
}
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if e := s.store.DeleteDevice(r.PathValue("id")); e != nil {
		apiError(w, 404, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) target(w http.ResponseWriter, r *http.Request) (Device, bool) {
	d, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		apiError(w, 404, errors.New("设备不存在"))
	}
	return d, ok
}
func (s *Server) wake(w http.ResponseWriter, r *http.Request) {
	d, ok := s.target(w, r)
	if !ok {
		return
	}
	if !s.rate(w, false) {
		return
	}
	e := Wake(r.Context(), d)
	result := "唤醒包已发送"
	if e != nil {
		result = "发送失败: " + e.Error()
	}
	warning := ""
	if le := s.store.Record(Event{time.Now().UTC().Format(time.RFC3339), d.ID, d.Name, result}); le != nil {
		warning = "操作记录保存失败"
		slog.Error(warning, "error", le)
	}
	if e != nil {
		apiError(w, 502, e)
		return
	}
	respond(w, 200, map[string]string{"message": result, "warning": warning})
}
func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	d, ok := s.target(w, r)
	if !ok {
		return
	}
	if !s.rate(w, false) {
		return
	}
	online, e := Probe(r.Context(), d)
	if e != nil {
		apiError(w, 400, e)
		return
	}
	message := "未检测到响应（不代表已关机）"
	if online {
		message = "设备已上线：TCP 端口可达"
	}
	respond(w, 200, map[string]any{"reachable": online, "message": message})
}
func ValidateListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("监听端口须为 1–65535")
	}
	return nil
}
