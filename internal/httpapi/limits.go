package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"time"
)

func (a *API) authLimit(w http.ResponseWriter, r *http.Request, username, action string, accountLimit int) bool {
	// Per-account state survives new sessions and is shared by every application instance.
	globalLimit := 60
	if action == "register" {
		globalLimit = 20
	}
	return a.limit(w, r, action+":user:"+limitKey(username), accountLimit, 10*time.Minute) &&
		a.limit(w, r, action+":ip:"+limitKey(clientIP(r)), 30, 10*time.Minute) &&
		a.limit(w, r, action+":global", globalLimit, time.Minute)
}

func (a *API) limit(w http.ResponseWriter, r *http.Request, key string, maximum int, window time.Duration) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var count int
	err := a.DB.QueryRow(ctx, `
  INSERT INTO request_limits(key,count,expires_at) VALUES($1,1,now()+make_interval(secs=>$2))
  ON CONFLICT(key) DO UPDATE SET
   count=CASE WHEN request_limits.expires_at<=now() THEN 1 ELSE LEAST(request_limits.count+1,$3+1) END,
   expires_at=CASE WHEN request_limits.expires_at<=now() THEN EXCLUDED.expires_at ELSE request_limits.expires_at END
  RETURNING count`, key, window.Seconds(), maximum).Scan(&count)
	if err != nil {
		a.internal(w, "rate limit", err)
		return false
	}
	if count > maximum {
		w.Header().Set("Retry-After", "60")
		problem(w, http.StatusTooManyRequests, "rate_limit", "Too many requests. Try again later.")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func limitKey(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
