package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter — на регистрацию: перебор кодов бессмыслен и при TTL,
// но пусть будет. Скользящее окно по IP, в памяти — сервис один.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
	calls  int // раз в sweepEvery вызовов — чистка протухших IP, иначе map растёт вечно
}

const sweepEvery = 1024

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (r *rateLimiter) allow(remoteAddr string) bool {
	ip := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = h
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls++; r.calls%sweepEvery == 0 {
		r.sweep(now)
	}
	kept := r.hits[ip][:0]
	for _, t := range r.hits[ip] {
		if now.Sub(t) < r.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[ip] = kept
		return false
	}
	r.hits[ip] = append(kept, now)
	return true
}

// sweep убирает IP, у которых все попытки старше окна.
func (r *rateLimiter) sweep(now time.Time) {
	for ip, ts := range r.hits {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= r.window {
			delete(r.hits, ip)
		}
	}
}

// clientAddr — за nginx настоящий адрес в X-Real-IP.
func clientAddr(req *http.Request) string {
	if ip := req.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return req.RemoteAddr
}
