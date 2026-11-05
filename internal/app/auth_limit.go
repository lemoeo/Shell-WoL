package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxAuthSources = 1024

type sourceWindow struct {
	expires time.Time
	count   int
}

// Fixed one-minute windows, independent for each endpoint and normalized IP.
// Expired entries are reclaimed lazily; no cleanup goroutine is needed.
type sourceLimiter struct {
	mu      sync.Mutex
	windows map[string]sourceWindow
	limit   int
	cleanup time.Time
}

func newSourceLimiter(limit int) *sourceLimiter {
	return &sourceLimiter{windows: make(map[string]sourceWindow), limit: limit}
}

// A zero delay means allowed. Rejected attempts never extend the window.
func (l *sourceLimiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.cleanup) {
		for k, window := range l.windows {
			if !now.Before(window.expires) {
				delete(l.windows, k)
			}
		}
		l.cleanup = now.Add(time.Minute)
	}
	window, exists := l.windows[key]
	if !exists && len(l.windows) >= maxAuthSources {
		// Do not evict active limits: IP churn must not reset blocked sources.
		// Reclaim any entries that expired since the last periodic cleanup.
		earliest := now.Add(time.Minute)
		for k, entry := range l.windows {
			if !now.Before(entry.expires) {
				delete(l.windows, k)
			} else if entry.expires.Before(earliest) {
				earliest = entry.expires
			}
		}
		if len(l.windows) >= maxAuthSources {
			return earliest.Sub(now)
		}
	}
	if !exists || !now.Before(window.expires) {
		window = sourceWindow{expires: now.Add(time.Minute)}
	}
	if window.count >= l.limit {
		return window.expires.Sub(now)
	}
	window.count++
	l.windows[key] = window
	return 0
}

func (s *Server) authRate(w http.ResponseWriter, r *http.Request, limiter *sourceLimiter) bool {
	delay := limiter.retryAfter(s.clientIP(r), time.Now())
	if delay == 0 {
		return true
	}
	seconds := int64((delay + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	apiError(w, http.StatusTooManyRequests, errors.New("请求过于频繁，请稍后重试"))
	return false
}

func passwordBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	apiError(w, http.StatusServiceUnavailable, errors.New("密码服务繁忙，请稍后重试"))
}

// Share a CPU bound across login and setup, without queueing HTTP requests.
func (s *Server) runPasswordWork(w http.ResponseWriter, work func() error) (error, bool) {
	select {
	case s.passwordWork <- struct{}{}:
		defer func() { <-s.passwordWork }()
		return work(), true
	default:
		passwordBusy(w)
		return nil, false
	}
}

func parseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	if len(entries) > 64 {
		return nil, errors.New("trusted_proxies 最多配置 64 个 IP 或 CIDR")
	}
	var result []netip.Prefix
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, addrErr := netip.ParseAddr(entry)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("trusted_proxies 中的 IP 或 CIDR 无效: %q", entry)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("trusted_proxies 的 IPv4 映射 CIDR 前缀须至少为 96: %q", entry)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func (s *Server) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.WithZone("").Unmap()
	if !s.trustedProxy(peer) {
		return peer.String()
	}
	// Cap parsing work and fail closed to the direct peer for malformed chains.
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if len(forwarded) == 0 || len(forwarded) > 4096 {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return peer.String()
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil || addr.Zone() != "" {
			return peer.String()
		}
		chain[i] = addr.Unmap()
	}
	// Each trusted hop vouches only for its immediate predecessor. Stop at the
	// nearest untrusted hop so an injected leftmost address cannot bypass limits.
	for i := len(chain) - 1; i >= 0; i-- {
		peer = chain[i]
		if !s.trustedProxy(peer) {
			break
		}
	}
	return peer.String()
}
