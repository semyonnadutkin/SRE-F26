package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type File struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Name        string    `json:"name"`
	StorageKey  string    `json:"storage_key"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const fileColumns = "id, user_id, name, storage_key, content_type, size_bytes, created_at, updated_at"

func (a *API) listFiles(w http.ResponseWriter, r *http.Request) {
	limit := 20
	var before int64
	var err error
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > 100 {
			problem(w, 400, "pagination", "Limit must be between 1 and 100.")
			return
		}
	}
	if s := r.URL.Query().Get("before"); s != "" {
		before, err = strconv.ParseInt(s, 10, 64)
		if err != nil || before < 1 {
			problem(w, 400, "pagination", "Before must be a positive file ID.")
			return
		}
	}
	rows, err := a.DB.Query(r.Context(), "SELECT "+fileColumns+` FROM files
  WHERE user_id=$1 AND state='ready' AND ($2::bigint=0 OR id<$2)
  ORDER BY id DESC LIMIT $3`, currentUser(r).ID, before, limit+1)
	if err != nil {
		a.internal(w, "list_files", err)
		return
	}
	defer rows.Close()

	items := make([]File, 0, limit+1)
	for rows.Next() {
		file, err := scanFile(rows)
		if err != nil {
			a.internal(w, "scan_files", err)
			return
		}
		items = append(items, file)
	}
	if err := rows.Err(); err != nil {
		a.internal(w, "list_files_rows", err)
		return
	}

	var next *int64
	if len(items) > limit {
		items = items[:limit]
		lastID := items[len(items)-1].ID
		next = &lastID
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_before": next, "max_file_bytes": a.Config.MaxFileBytes})
}

func (a *API) getFile(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(w, r)
	if !ok {
		return
	}
	file, err := scanFile(a.DB.QueryRow(r.Context(), "SELECT "+fileColumns+" FROM files WHERE id=$1 AND user_id=$2 AND state='ready'", id, currentUser(r).ID))
	if err != nil {
		a.fileError(w, err, "get_file")
		return
	}
	writeJSON(w, 200, file)
}

func (a *API) uploadFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !validFileName(name) {
		problem(w, 400, "name", "File name must contain 1-255 characters without path separators.")
		return
	}
	if r.ContentLength > a.Config.MaxFileBytes {
		problem(w, 413, "file_too_large", "File exceeds the size limit.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxFileBytes+1)
	limited := &sizeReader{reader: r.Body, remaining: a.Config.MaxFileBytes}
	reader := bufio.NewReader(limited)
	sample, err := reader.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(err, errFileTooLarge) {
			problem(w, 413, "file_too_large", "File exceeds the size limit.")
		} else {
			problem(w, 400, "upload", "Could not read the upload.")
		}
		return
	}

	contentType := http.DetectContentType(sample)
	key, err := randomHex(32)
	if err != nil {
		a.internal(w, "storage_key", err)
		return
	}
	key = "fileserver/" + key

	// Commit the reservation before S3 writes so a crash leaves a recoverable operation.
	file, err := scanFile(a.DB.QueryRow(r.Context(), `INSERT INTO files(user_id,name,storage_key,content_type,size_bytes,state)
  VALUES($1,$2,$3,$4,0,'uploading') RETURNING `+fileColumns, currentUser(r).ID, name, key, contentType))
	if err != nil {
		a.internal(w, "reserve_upload", err)
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		a.internal(w, "upload_lock", err)
		return
	}
	defer tx.Rollback(context.Background())
	var state string
	err = tx.QueryRow(r.Context(), "SELECT state FROM files WHERE id=$1 FOR UPDATE", file.ID).Scan(&state)
	if err != nil {
		a.internal(w, "upload_lock", err)
		return
	}
	if state != "uploading" {
		a.internal(w, "upload state", errors.New("unexpected file state"))
		return
	}

	sizeHint := r.ContentLength
	if sizeHint < 0 {
		sizeHint = -1
	}

	size, err := a.Store.Put(
		r.Context(),
		key,
		contentType,
		reader,
		sizeHint,
	)

	if err != nil {
		// Keep the durable uploading row for cleanup. S3 SDK attempts AbortMultipartUpload on failure.
		if errors.Is(err, errFileTooLarge) {
			problem(w, 413, "file_too_large", "File exceeds the size limit.")
		} else {
			a.internal(w, "upload_object", err)
		}
		return
	}

	file, err = scanFile(tx.QueryRow(r.Context(), "UPDATE files SET size_bytes=$1,state='ready',updated_at=now() WHERE id=$2 RETURNING "+fileColumns, size, file.ID))
	if err != nil {
		a.internal(w, "finish_upload", err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.internal(w, "upload_commit", err)
		return
	}

	w.Header().Set("Location", "/api/files/"+strconv.FormatInt(file.ID, 10))
	writeJSON(w, 201, file)
}

func (a *API) renameFile(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(w, r)
	if !ok {
		return
	}

	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validFileName(input.Name) {
		problem(w, 400, "name", "File name must contain 1–255 characters without path separators.")
		return
	}

	file, err := scanFile(a.DB.QueryRow(r.Context(), `UPDATE files SET name=$1,updated_at=now()
  WHERE id=$2 AND user_id=$3 AND state='ready' RETURNING `+fileColumns, input.Name, id, currentUser(r).ID))
	if err != nil {
		a.fileError(w, err, "rename_file")
		return
	}
	writeJSON(w, 200, file)
}

func (a *API) downloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(w, r)
	if !ok {
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		a.internal(w, "download", err)
		return
	}
	defer tx.Rollback(context.Background())
	// A shared row lock prevents deletion halfway through a live download.
	file, err := scanFile(tx.QueryRow(r.Context(), "SELECT "+fileColumns+" FROM files WHERE id=$1 AND user_id=$2 AND state='ready' FOR SHARE", id, currentUser(r).ID))
	if err != nil {
		a.fileError(w, err, "download_metadata")
		return
	}
	object, err := a.Store.Get(r.Context(), file.StorageKey)
	if err != nil {
		a.internal(w, "download_object", err)
		return
	}
	defer object.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	if _, err = io.Copy(w, object); err != nil {
		a.Log.Error("download interrupted", "file_id", file.ID)
		return
	}
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(w, r)
	if !ok {
		return
	}
	// Persist deletion intent; retries can finish it even when S3 or the process fails.
	var key string
	err := a.DB.QueryRow(r.Context(), `UPDATE files SET state='deleting',updated_at=now()
  WHERE id=$1 AND user_id=$2 AND state IN ('ready','deleting') RETURNING storage_key`, id, currentUser(r).ID).Scan(&key)
	if err != nil {
		a.fileError(w, err, "mark_delete")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		a.internal(w, "delete", err)
		return
	}
	defer tx.Rollback(context.Background())
	err = tx.QueryRow(r.Context(), "SELECT storage_key FROM files WHERE id=$1 AND user_id=$2 AND state='deleting' FOR UPDATE", id, currentUser(r).ID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		a.internal(w, "delete_lock", err)
		return
	}
	if err = a.Store.Delete(r.Context(), key); err != nil {
		a.internal(w, "delete_object", err)
		return
	}
	_, err = tx.Exec(r.Context(), "DELETE FROM files WHERE id=$1", id)
	if err != nil {
		a.internal(w, "delete_metadata", err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.internal(w, "delete_commit", err)
		return
	}
	w.WriteHeader(204)
}

func scanFile(row pgx.Row) (File, error) {
	var file File
	err := row.Scan(&file.ID, &file.UserID, &file.Name, &file.StorageKey, &file.ContentType, &file.Size, &file.CreatedAt, &file.UpdatedAt)
	return file, err
}

func fileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w)
		return 0, false
	}
	return id, true
}

func notFound(w http.ResponseWriter) {
	problem(w, http.StatusNotFound, "not_found", "File not found.")
}

func (a *API) fileError(w http.ResponseWriter, err error, op string) {
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
	} else {
		a.internal(w, op, err)
	}
}
