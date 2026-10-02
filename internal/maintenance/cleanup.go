package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/semyonnadutkin/SRE-F26/internal/storage"
)

// Cleanup processes only durable operations owned by this application.
// An age threshold AND row locks protect active uploads/downloads on every replica.
func Cleanup(ctx context.Context, db *pgxpool.Pool, s *storage.Store, log *slog.Logger) error {
	for _, table := range []string{"sessions", "request_limits"} {
		tag, err := db.Exec(ctx, "DELETE FROM "+table+" WHERE expires_at < now()")
		if err != nil {
			return errors.New("expired record cleanup failed")
		}
		log.Info("expired records removed", "table", table, "count", tag.RowsAffected())
	}
	for count := 0; count < 500; count++ {
		removed, err := cleanupFile(ctx, db, s, log)
		if err != nil {
			return err
		}
		if !removed {
			return nil
		}
	}
	log.Info("cleanup batch limit reached; run again")
	return nil
}

func cleanupFile(ctx context.Context, db *pgxpool.Pool, store *storage.Store, log *slog.Logger) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, errors.New("begin file cleanup failed")
	}
	defer tx.Rollback(context.Background())

	var id int64
	var key string
	err = tx.QueryRow(ctx, `
  SELECT id,storage_key FROM files
  WHERE state <> 'ready' AND updated_at < now()-interval '1 hour' AND storage_key LIKE 'fileserver/%'
  ORDER BY updated_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("select pending file failed")
	}

	if err := store.Client.RemoveIncompleteUpload(ctx, store.Bucket, key); err != nil {
		return false, errors.New("abort multipart upload failed; pending file retained")
	}
	if err := store.Delete(ctx, key); err != nil {
		return false, errors.New("remove object failed; pending file retained")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM files WHERE id=$1", id); err != nil {
		return false, errors.New("remove pending metadata failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, errors.New("commit file cleanup failed")
	}
	log.Info("pending file removed", "file_id", id)
	return true, nil
}

// Audit never deletes unknown objects. Restore of a DB backup can make a valid object look orphaned.
func Audit(ctx context.Context, db *pgxpool.Pool, s *storage.Store, log *slog.Logger) error {
	rows, err := db.Query(ctx, "SELECT id,storage_key,state FROM files")
	if err != nil {
		return errors.New("audit query failed")
	}
	type entry struct {
		id    int64
		state string
	}
	known := map[string]entry{}
	for rows.Next() {
		var id int64
		var key, state string
		if rows.Scan(&id, &key, &state) != nil {
			rows.Close()
			return errors.New("audit scan failed")
		}
		known[key] = entry{id, state}
		if state != "ready" {
			log.Info("unfinished file", "file_id", id, "state", state)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("audit rows failed")
	}
	for key, e := range known {
		if e.state == "ready" {
			_, err := s.Client.StatObject(ctx, s.Bucket, key, minio.StatObjectOptions{})
			if err != nil {
				if minio.ToErrorResponse(err).Code == "NoSuchKey" {
					log.Warn("ready object missing", "file_id", e.id)
				} else {
					return errors.New("S3 audit unavailable")
				}
			}
		}
	}
	for o := range s.Client.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: "fileserver/", Recursive: true}) {
		if o.Err != nil {
			return errors.New("object audit failed")
		}
		if _, ok := known[o.Key]; !ok && strings.HasPrefix(o.Key, "fileserver/") {
			log.Warn("untracked object; manual review required", "storage_key", o.Key)
		}
	}
	return nil
}
