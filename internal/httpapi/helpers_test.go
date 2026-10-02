package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/semyonnadutkin/SRE-F26/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestSizeReader(t *testing.T) {
	for _, size := range []int{0, 1, 7, 8, 9, 10000} {
		reader := &sizeReader{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, size)), remaining: 8}
		body, err := io.ReadAll(reader)
		if size > 8 {
			if !errors.Is(err, errFileTooLarge) || len(body) != 8 {
				t.Fatalf("size %d: read %d, error %v", size, len(body), err)
			}
		} else if err != nil || len(body) != size {
			t.Fatalf("size %d: %v", size, err)
		}
	}
}

func TestFileNames(t *testing.T) {
	for _, name := range []string{"", "  ", "a/b", "a\\b", "a\r\nx", strings.Repeat("a", 256)} {
		if validFileName(name) {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
	if !validFileName("report-\u00e9.txt") || !validFileName("<script>.txt") {
		t.Fatal("valid file name rejected; escaping belongs at the output boundary")
	}
}

func TestStrictJSON(t *testing.T) {
	for _, body := range []string{`{"username":"alice"} {}`, `{"username":"alice","unknown":1}`, strings.Repeat("x", 5000)} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		var input struct {
			Username string `json:"username"`
		}
		if decode(response, request, &input) || response.Code != http.StatusBadRequest {
			t.Fatal("invalid JSON accepted")
		}
	}
}

func TestSessionTokenAndCookie(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, err := randomHex(32)
		if err != nil || len(token) != 64 || seen[token] || len(tokenHash(token)) != 32 {
			t.Fatal("invalid session token")
		}
		seen[token] = true
	}
	api := &API{Config: config.Config{SessionTTL: time.Hour, CookieSecure: true}}
	response := httptest.NewRecorder()
	api.setSessionCookie(response, strings.Repeat("a", 64))
	cookie := response.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 3600 {
		t.Fatal("unsafe session cookie")
	}
	response = httptest.NewRecorder()
	api.setSessionCookie(response, "")
	if response.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout must clear the cookie")
	}
}

func TestDummyPasswordHash(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	if err != nil || cost != 12 {
		t.Fatal("missing-user check must use a valid cost-12 hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte("a deliberately wrong password")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatal("dummy hash must perform a real password comparison")
	}
}
