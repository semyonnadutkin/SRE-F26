package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/semyonnadutkin/SRE-F26/internal/auth"
)

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userContextKey struct{}

// A valid cost-12 hash makes a missing user require the same bcrypt work as a wrong password.
const dummyPasswordHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	defer observeOperation("register")()

	input, ok := readCredentials(w, r)
	if !ok || !a.authLimit(w, r, input.Username, "register", 5) {
		return
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		a.internal(w, "hash password", err)
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		a.internal(w, "register", err)
		return
	}
	defer tx.Rollback(context.Background())

	user, err := scanUser(tx.QueryRow(r.Context(),
		"INSERT INTO users(username,password_hash) VALUES($1,$2) RETURNING id,username,created_at", input.Username, hash))
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			problem(w, http.StatusConflict, "username_taken", "Username is unavailable.")
		} else {
			a.internal(w, "create user", err)
		}
		return
	}
	token, err := a.createSession(r.Context(), tx, user.ID)
	if err != nil {
		a.internal(w, "create session", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		a.internal(w, "commit registration", err)
		return
	}

	registrationsTotal.Inc()

	a.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	defer observeOperation("login")()

	input, ok := readCredentials(w, r)
	if !ok || !a.authLimit(w, r, input.Username, "login", 10) {
		return
	}

	var user User
	var hash string
	err := a.DB.QueryRow(r.Context(),
		"SELECT id,username,created_at,password_hash FROM users WHERE username=$1", input.Username).
		Scan(&user.ID, &user.Username, &user.CreatedAt, &hash)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		a.internal(w, "find user", err)
		return
	}
	if missing {
		hash = dummyPasswordHash
	}
	matches := auth.PasswordMatches(hash, input.Password)
	if missing || !matches {
		problem(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		a.internal(w, "login", err)
		return
	}
	defer tx.Rollback(context.Background())
	token, err := a.createSession(r.Context(), tx, user.ID)
	if err != nil {
		a.internal(w, "create session", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		a.internal(w, "commit login", err)
		return
	}

	loginsTotal.Inc()

	a.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, user)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("session")
	if _, err := a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookie.Value)); err != nil {
		a.internal(w, "logout", err)
		return
	}
	a.setSessionCookie(w, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

func (a *API) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || len(cookie.Value) != 64 {
			problem(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
			return
		}

		user, err := scanUser(a.DB.QueryRow(r.Context(), `
   SELECT u.id,u.username,u.created_at FROM sessions s JOIN users u ON u.id=s.user_id
   WHERE s.token_hash=$1 AND s.expires_at>now()`, tokenHash(cookie.Value)))
		if errors.Is(err, pgx.ErrNoRows) {
			problem(w, http.StatusUnauthorized, "unauthorized", "Session expired. Sign in again.")
			return
		}
		if err != nil {
			a.internal(w, "authenticate", err)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var input credentials
	if !decode(w, r, &input) {
		return input, false
	}
	username, err := auth.NormalizeUsername(input.Username)
	if err != nil {
		problem(w, http.StatusBadRequest, "username", err.Error())
		return input, false
	}
	input.Username = username
	if err := auth.ValidatePassword(input.Password); err != nil {
		problem(w, http.StatusBadRequest, "password", err.Error())
		return input, false
	}
	return input, true
}

func (a *API) createSession(ctx context.Context, tx pgx.Tx, userID int64) (string, error) {
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+make_interval(secs=>$3))",
		tokenHash(token), userID, a.Config.SessionTTL.Seconds())
	return token, err
}

func (a *API) setSessionCookie(w http.ResponseWriter, token string) {
	cookie := &http.Cookie{
		Name: "session", Value: token, Path: "/", HttpOnly: true,
		Secure: a.Config.CookieSecure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(a.Config.SessionTTL.Seconds()), Expires: time.Now().Add(a.Config.SessionTTL),
	}
	if token == "" {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}

func scanUser(row pgx.Row) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.Username, &user.CreatedAt)
	return user, err
}

func currentUser(r *http.Request) User {
	return r.Context().Value(userContextKey{}).(User)
}
