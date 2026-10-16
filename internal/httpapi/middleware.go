package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/minio/minio-go/v7"
)

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		defer func() {
			a.Log.Info("HTTP request", "method", r.Method, "route", r.Pattern,
				"duration_ms", float64(time.Since(started).Microseconds())/1000)
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		defer func() {
			if recover() != nil {
				a.Log.Error("handler panic")
				problem(w, http.StatusInternalServerError, "internal", "Internal server error.")
			}
		}()

		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if !allowedOrigin(r, a.Config.AppOrigin) {
				problem(w, http.StatusForbidden, "origin", "Request origin is not allowed.")
				return
			}

			timeout := 15 * time.Second
			if (r.Method == http.MethodPost && r.URL.Path == "/api/files") || strings.HasSuffix(r.URL.Path, "/download") {
				timeout = 10 * time.Minute
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			r = r.WithContext(ctx)

			// Forwarded headers are untrusted; only the TCP peer determines the IP limit.
			if !a.limit(w, r, "api:ip:"+limitKey(clientIP(r)), 300, time.Minute) ||
				!a.limit(w, r, "api:global", 10000, time.Minute) {
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (a *API) internal(w http.ResponseWriter, operation string, err error) {
	// Log useful error classes without database credentials, SQL values, or provider messages.
	cause := fmt.Sprintf("%T", err)
	var databaseError *pgconn.PgError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cause = "timeout"
	case errors.Is(err, context.Canceled):
		cause = "canceled"
	case errors.As(err, &databaseError):
		cause = "PostgreSQL " + databaseError.Code
	default:
		if storageError := minio.ToErrorResponse(err); storageError.Code != "" {
			cause = "S3 " + storageError.Code
		}
	}
	a.Log.Error("request failed", "operation", operation, "cause", cause)
	problem(w, http.StatusServiceUnavailable, "unavailable", "Service unavailable. Try again later.")
}

func allowedOrigin(r *http.Request, configured string) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	expected := parseOrigin(configured)
	if expected == nil {
		return false
	}
	target := parseOrigin(expected.Scheme + "://" + r.Host)
	if target == nil {
		return false
	}
	if target.Host != expected.Host {
		// Local HTTP accepts loopback aliases on the configured port, never arbitrary hosts.
		if expected.Scheme != "http" || !loopbackHost(expected.Hostname()) || !loopbackHost(target.Hostname()) || target.Port() != expected.Port() {
			return false
		}
	}
	if r.Header.Get("Origin") == "" {
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	source := parseOrigin(r.Header.Get("Origin"))
	return source != nil && source.Scheme == target.Scheme && source.Host == target.Host
}

func parseOrigin(value string) *url.URL {
	origin, err := url.Parse(value)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Hostname() == "" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil
	}
	port := origin.Port()
	if port == "" {
		port = "80"
		if origin.Scheme == "https" {
			port = "443"
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil
	}
	origin.Host = net.JoinHostPort(strings.ToLower(origin.Hostname()), strconv.Itoa(number))
	return origin
}

func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
