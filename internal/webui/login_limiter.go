package webui

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginMaxFails    = 5
	loginBlockPeriod = 10 * time.Minute
	loginMaxTracked  = 4096
)

type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]int
	until map[string]time.Time
	seen  map[string]time.Time
}

var limiter = newLoginLimiter()

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string]int{}, until: map[string]time.Time{}, seen: map[string]time.Time{}}
}

func (l *loginLimiter) initLocked() {
	if l.fails == nil {
		l.fails = map[string]int{}
	}
	if l.until == nil {
		l.until = map[string]time.Time{}
	}
	if l.seen == nil {
		l.seen = map[string]time.Time{}
	}
}

func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	if blockedUntil, ok := l.until[ip]; ok {
		if left := time.Until(blockedUntil); left > 0 {
			return true, left
		}
		delete(l.until, ip)
		delete(l.fails, ip)
		delete(l.seen, ip)
	}
	return false, 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	if _, exists := l.fails[ip]; !exists && len(l.fails) >= loginMaxTracked {
		var oldestIP string
		var oldest time.Time
		for candidate := range l.fails {
			lastSeen := l.seen[candidate]
			if oldestIP == "" || lastSeen.Before(oldest) {
				oldestIP, oldest = candidate, lastSeen
			}
		}
		delete(l.fails, oldestIP)
		delete(l.until, oldestIP)
		delete(l.seen, oldestIP)
	}
	now := time.Now()
	l.fails[ip]++
	l.seen[ip] = now
	if l.fails[ip] >= loginMaxFails {
		l.until[ip] = now.Add(loginBlockPeriod)
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	delete(l.fails, ip)
	delete(l.until, ip)
	delete(l.seen, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
