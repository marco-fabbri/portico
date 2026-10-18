package proxy

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type ruleKey struct{}

// ruleHolder lets the policy step name the rule that decided the request,
// so the access log can print it.
type ruleHolder struct{ rule string }

func withRuleHolder(r *http.Request) (*http.Request, *ruleHolder) {
	h := &ruleHolder{}
	return r.WithContext(context.WithValue(r.Context(), ruleKey{}, h)), h
}

func setRule(r *http.Request, rule string) {
	if h, ok := r.Context().Value(ruleKey{}).(*ruleHolder); ok {
		h.rule = rule
	}
}

// statusRecorder captures the status and size for the log and keeps
// Hijacker and Flusher reachable for WebSockets and streaming.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		if s.status == 0 {
			s.status = http.StatusSwitchingProtocols
		}
		return hj.Hijack()
	}
	return nil, nil, errors.New("response writer cannot be hijacked")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one line per request: host, method, path (never the
// query), status, bytes, duration, the rule that decided it and whether a
// session was present. Header values are never logged.
func AccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		r, holder := withRuleHolder(r)
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		rule := holder.rule
		switch {
		case rule != "":
		case rec.status == http.StatusUnauthorized,
			rec.status == http.StatusFound && strings.HasPrefix(w.Header().Get("Location"), "/.portico/login"):
			rule = "no-session"
		default:
			rule = "ok"
		}
		_, hasSession := r.Cookie(sessionCookieName)
		log.Info("request",
			"host", r.Host,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"rule", rule,
			"session", hasSession == nil,
			"auth_scheme", authScheme(r),
		)
	})
}

// authScheme returns the lower-cased scheme of the first Authorization
// value ("basic", "bearer", ...) or "none"; the credential itself is never
// returned.
func authScheme(r *http.Request) string {
	if f := strings.Fields(r.Header.Get("Authorization")); len(f) > 0 {
		return strings.ToLower(f[0])
	}
	return "none"
}
