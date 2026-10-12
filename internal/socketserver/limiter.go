package socketserver

import (
	"sync"
	"time"
)

type attempt struct {
	count   int
	expires time.Time
}
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]attempt
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for key, a := range l.entries {
		if !now.Before(a.expires) {
			delete(l.entries, key)
		}
	}
	a, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= 4096 {
			return false
		}
		a.expires = now.Add(time.Minute)
	}
	if a.count >= 10 {
		return false
	}
	a.count++
	l.entries[ip] = a
	return true
}
