package handler

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxRateLimitVisitors = 10000

// RateLimiter implements a per-client-IP token bucket rate limiter.
type RateLimiter struct {
	mu             sync.Mutex
	visitors       map[string]*tokenBucket
	rate           float64 // tokens per second
	burst          int     // max tokens
	trustedProxies []*net.IPNet
	stop           chan struct{}
	closeOnce      sync.Once
}

type tokenBucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter accepts optional trusted proxy IPs or CIDRs. X-Forwarded-For is ignored unless
// the direct peer is trusted, preventing callers from selecting a fresh rate-limit bucket.
func NewRateLimiter(ratePerSec float64, burst int, trustedProxies ...string) *RateLimiter {
	rl := &RateLimiter{
		visitors:       make(map[string]*tokenBucket),
		rate:           ratePerSec,
		burst:          burst,
		trustedProxies: parseTrustedProxies(trustedProxies),
		stop:           make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visitors[ip]
	now := time.Now()
	if !ok {
		if len(rl.visitors) >= maxRateLimitVisitors {
			return false
		}
		v = &tokenBucket{tokens: float64(rl.burst), lastCheck: now}
		rl.visitors[ip] = v
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(v.lastCheck).Seconds()
	v.tokens += elapsed * rl.rate
	if v.tokens > float64(rl.burst) {
		v.tokens = float64(rl.burst)
	}
	v.lastCheck = now

	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stop:
			return
		}
	}
}

// Close stops the limiter's maintenance goroutine.
func (rl *RateLimiter) Close() {
	rl.closeOnce.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, v := range rl.visitors {
		if now.Sub(v.lastCheck) > 5*time.Minute {
			delete(rl.visitors, ip)
		}
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.Allow(rl.clientIP(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) clientIP(r *http.Request) string {
	remote := parseRemoteIP(r.RemoteAddr)
	if remote == nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if !rl.isTrustedProxy(remote) {
		return remote.String()
	}

	// Walk the proxy chain from the trusted peer toward the client. The first untrusted hop is
	// the effective client. This resists a caller prepending arbitrary X-Forwarded-For values.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate == nil {
			continue
		}
		if !rl.isTrustedProxy(candidate) {
			return candidate.String()
		}
		remote = candidate
	}
	return remote.String()
}

func (rl *RateLimiter) isTrustedProxy(ip net.IP) bool {
	for _, network := range rl.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddr), "[]")
	}
	return net.ParseIP(host)
}

func parseTrustedProxies(values []string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(value); err == nil {
			networks = append(networks, network)
			continue
		}
		if ip := net.ParseIP(value); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip = ip.To4()
				bits = 32
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return networks
}
