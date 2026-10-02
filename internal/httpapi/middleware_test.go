package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/semyonnadutkin/SRE-F26/internal/config"
)

func TestRoutesAndOriginProtection(t *testing.T) {
	handler := NewHandler(&API{Config: config.Config{AppOrigin: "http://localhost"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, path := range []string{"/healthz", "/", "/app.js", "/style.css"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, response.Code)
		}
	}
	for _, origin := range []string{"", "https://another.example.test"} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/login", nil)
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatal("unsafe origin reached the database")
		}
	}
}

func TestBrowserOrigins(t *testing.T) {
	cases := []struct {
		name, configured, host, origin, site, method string
		allowed                                      bool
	}{
		{"localhost", "http://localhost", "localhost", "http://localhost", "same-origin", "POST", true},
		{"explicit port in host", "http://localhost", "localhost:80", "http://localhost", "same-origin", "POST", true},
		{"explicit port in origin", "http://localhost", "localhost", "http://localhost:80", "same-origin", "POST", true},
		{"loopback IP", "http://localhost", "127.0.0.1", "http://127.0.0.1", "same-origin", "POST", true},
		{"IPv6 loopback", "http://localhost", "[::1]", "http://[::1]", "same-origin", "POST", true},
		{"development port", "http://localhost:9000", "127.0.0.1:9000", "http://127.0.0.1:9000", "", "POST", true},
		{"HTTPS default port", "https://files.example.test", "files.example.test:443", "https://files.example.test", "", "POST", true},
		{"read without origin", "http://localhost", "127.0.0.1:80", "", "", "GET", true},
		{"mutation without origin", "http://localhost", "localhost", "", "", "POST", false},
		{"opaque origin", "http://localhost", "localhost", "null", "", "POST", false},
		{"cross-site fetch", "http://localhost", "localhost", "http://localhost", "cross-site", "POST", false},
		{"cross-host source", "http://localhost", "localhost", "http://127.0.0.1", "", "POST", false},
		{"wrong port", "http://localhost", "localhost:81", "http://localhost:81", "", "POST", false},
		{"different scheme", "http://localhost", "localhost", "https://localhost", "", "POST", false},
		{"host suffix attack", "http://localhost", "localhost.attacker.test", "http://localhost.attacker.test", "", "POST", false},
		{"origin suffix attack", "http://localhost", "localhost", "http://localhost.attacker.test", "", "POST", false},
		{"invalid origin path", "http://localhost", "localhost", "http://localhost/", "", "POST", false},
		{"invalid origin credentials", "http://localhost", "localhost", "http://user@localhost", "", "POST", false},
		{"production rejects loopback", "https://files.example.test", "localhost", "https://localhost", "", "POST", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/api/auth/login", nil)
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.site)
			if allowedOrigin(request, test.configured) != test.allowed {
				t.Fatalf("origin allowed: want %v", test.allowed)
			}
		})
	}
}
