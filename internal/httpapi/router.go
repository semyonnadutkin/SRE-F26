package httpapi

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/semyonnadutkin/SRE-F26/internal/config"
	"github.com/semyonnadutkin/SRE-F26/internal/storage"
)

//go:embed static/*
var assets embed.FS

type API struct {
	DB     *pgxpool.Pool
	Store  *storage.Store
	Config config.Config
	Log    *slog.Logger
}

func NewHandler(api *API) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("POST /api/auth/register", api.register)
	mux.HandleFunc("POST /api/auth/login", api.login)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("POST /api/auth/logout", api.requireUser(api.logout))
	mux.Handle("GET /api/me", api.requireUser(api.me))
	mux.Handle("GET /api/files", api.requireUser(api.listFiles))
	mux.Handle("POST /api/files", api.requireUser(api.uploadFile))
	mux.Handle("GET /api/files/{id}", api.requireUser(api.getFile))
	mux.Handle("GET /api/files/{id}/download", api.requireUser(api.downloadFile))
	mux.Handle("PATCH /api/files/{id}", api.requireUser(api.renameFile))
	mux.Handle("DELETE /api/files/{id}", api.requireUser(api.deleteFile))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "not_found", "Route not found.")
	})

	static, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			problem(w, http.StatusMethodNotAllowed, "method", "Method not allowed.")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "."
		}

		if _, err := fs.Stat(static, name); errors.Is(err, fs.ErrNotExist) {
			page, err := fs.ReadFile(static, "404.html")
			if err != nil {
				api.internal(w, "read_404_page", err)
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if r.Method != http.MethodHead {
				_, _ = w.Write(page)
			}
			return
		}
		files.ServeHTTP(w, r)
	})
	return api.middleware(mux)
}

func health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"health": "ok"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var migrated bool
	err := a.DB.QueryRow(ctx, `
  SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=1 ORDER BY id DESC LIMIT 1),false)`).Scan(&migrated)
	if err != nil || !migrated || a.DB.Ping(ctx) != nil || a.Store.Ready(ctx) != nil {
		problem(w, http.StatusServiceUnavailable, "not_ready", "Database, schema, or storage is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ready": "ok"})
}
