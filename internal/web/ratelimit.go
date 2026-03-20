package web

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"golang.org/x/time/rate"
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	rate     float64
}

type RateLimitStore struct {
	ipLimiters    sync.Map
	tokenLimiters sync.Map
	loginLimiters sync.Map
	settingsSvc   *service.SettingsService
	log           *slog.Logger
}

func NewRateLimitStore(settingsSvc *service.SettingsService, log *slog.Logger) *RateLimitStore {
	s := &RateLimitStore{settingsSvc: settingsSvc, log: log}
	go s.cleanup()
	return s
}

func (s *RateLimitStore) clientIP(r *http.Request) string {
	if s.settingsSvc.GetTrustProxyHeaders() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); ip != "" {
				return ip
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func getOrCreateLimiter(store *sync.Map, key string, ratePerSec float64, burst int) *rate.Limiter {
	if v, ok := store.Load(key); ok {
		entry := v.(*limiterEntry)
		entry.lastSeen = time.Now()
		// Replace limiter if configured rate changed
		if entry.rate != ratePerSec {
			l := rate.NewLimiter(rate.Limit(ratePerSec), burst)
			newEntry := &limiterEntry{limiter: l, lastSeen: time.Now(), rate: ratePerSec}
			store.Store(key, newEntry)
			return l
		}
		return entry.limiter
	}
	l := rate.NewLimiter(rate.Limit(ratePerSec), burst)
	entry := &limiterEntry{limiter: l, lastSeen: time.Now(), rate: ratePerSec}
	store.Store(key, entry)
	return l
}

func (s *RateLimitStore) PerIPMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.settingsSvc.GetRateLimitEnabled() {
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}

			rps := float64(s.settingsSvc.GetRateLimitPerIP())
			ip := s.clientIP(r)
			limiter := getOrCreateLimiter(&s.ipLimiters, ip, rps, int(rps*2))

			if !limiter.Allow() {
				s.log.Warn("per-ip rate limit exceeded", "ip", ip, "path", r.URL.Path)
				handleRateLimited(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *RateLimitStore) PerTokenMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.settingsSvc.GetRateLimitEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			token := service.TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}

			rps := float64(s.settingsSvc.GetRateLimitPerToken())
			limiter := getOrCreateLimiter(&s.tokenLimiters, token.ID, rps, int(rps*2))

			if !limiter.Allow() {
				s.log.Warn("per-token rate limit exceeded", "token_id", token.ID, "path", r.URL.Path)
				handleRateLimited(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *RateLimitStore) LoginRateLimitMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.settingsSvc.GetRateLimitEnabled() || r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}

			perMin := float64(s.settingsSvc.GetRateLimitLogin())
			rps := perMin / 60.0
			burst := int(perMin / 2)
			if burst < 1 {
				burst = 1
			}

			ip := s.clientIP(r)
			limiter := getOrCreateLimiter(&s.loginLimiters, ip, rps, burst)

			if !limiter.Allow() {
				s.log.Warn("login rate limit exceeded", "ip", ip)
				handleRateLimited(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func handleRateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
	} else {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	}
}

func (s *RateLimitStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		cleaned := 0

		for _, store := range []*sync.Map{&s.ipLimiters, &s.tokenLimiters, &s.loginLimiters} {
			store.Range(func(key, value any) bool {
				entry := value.(*limiterEntry)
				if entry.lastSeen.Before(cutoff) {
					store.Delete(key)
					cleaned++
				}
				return true
			})
		}

		if cleaned > 0 {
			s.log.Debug("rate limiter cleanup", "removed", cleaned)
		}
	}
}
