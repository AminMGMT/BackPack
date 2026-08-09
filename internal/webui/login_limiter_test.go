package webui

import (
	"strconv"
	"testing"
)

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	ip := "203.0.113.9"

	for i := 0; i < loginMaxFails-1; i++ {
		l.fail(ip)
		if blocked, _ := l.blocked(ip); blocked {
			t.Fatalf("blocked after %d failures", i+1)
		}
	}
	l.fail(ip)
	if blocked, _ := l.blocked(ip); !blocked {
		t.Fatal("not blocked after reaching the limit")
	}

	l.reset(ip)
	if blocked, _ := l.blocked(ip); blocked {
		t.Fatal("still blocked after reset")
	}
}

func TestLoginLimiterStaysBounded(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginMaxTracked+100; i++ {
		l.fail(strconv.Itoa(i))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) > loginMaxTracked || len(l.seen) > loginMaxTracked || len(l.until) > loginMaxTracked {
		t.Fatalf("limiter grew past cap: fails=%d seen=%d until=%d", len(l.fails), len(l.seen), len(l.until))
	}
}
