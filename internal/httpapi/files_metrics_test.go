package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/semyonnadutkin/SRE-F26/internal/config"
	"github.com/semyonnadutkin/SRE-F26/internal/storage"
)

// These tests compare counter deltas and must not run in parallel.
func TestUploadBytes(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tc := range []struct {
		name       string
		beginErr   error
		putErr     error
		updateErr  error
		commitErr  error
		writeErr   error
		wantBytes  float64
		wantStatus int
	}{
		{name: "success uses Put size", wantBytes: 7, wantStatus: 201},
		{name: "begin failure", beginErr: failure, wantStatus: 503},
		{name: "S3 failure with nonzero size", putErr: failure, wantStatus: 503},
		{name: "metadata update failure", updateErr: failure, wantStatus: 503},
		{name: "commit failure", commitErr: failure, wantStatus: 503},
		{name: "JSON write failure after commit", writeErr: failure, wantBytes: 7, wantStatus: 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counterValue(t, uploadedBytesTotal)
			outcome := "failed"
			if tc.wantStatus == http.StatusCreated && tc.writeErr == nil {
				outcome = "success"
			}
			operation := operationsTotal.WithLabelValues("upload", outcome, strconv.Itoa(tc.wantStatus))
			operationBefore := counterValue(t, operation)
			committed := false
			updated := false
			putCalled := false
			tx := &bytesTestTx{
				queryRow: func(query string, args ...any) pgx.Row {
					if strings.HasPrefix(query, "SELECT state") {
						return bytesTestRow{values: []any{"uploading"}}
					}
					if !strings.Contains(query, "state='ready'") || args[0] != int64(7) {
						t.Fatalf("unexpected metadata update: %s %v", query, args)
					}
					updated = true
					if tc.updateErr != nil {
						return bytesTestRow{err: tc.updateErr}
					}
					return bytesFileRow(7)
				},
				commit: func() error {
					if !updated {
						t.Fatal("commit before metadata update")
					}
					if counterValue(t, uploadedBytesTotal) != before {
						t.Fatal("uploaded bytes counted before successful commit")
					}
					committed = tc.commitErr == nil
					return tc.commitErr
				},
			}
			api := bytesTestAPI(&bytesTestDB{tx: tx, beginErr: tc.beginErr}, &bytesTestStore{
				put: func(reader io.Reader, sizeHint int64) (int64, error) {
					putCalled = true
					body, err := io.ReadAll(reader)
					if err != nil || string(body) != "payload" || sizeHint != -1 {
						t.Fatalf("unexpected upload: %q, hint %d, error %v", body, sizeHint, err)
					}
					return int64(len(body)), tc.putErr
				},
			})
			r := bytesTestRequest(http.MethodPost, "/api/files?name=test.txt", strings.NewReader("payload"))
			r.ContentLength = -1 // The metric must use the size returned by Put.
			w := &bytesTestWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: tc.writeErr}
			api.uploadFile(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := counterValue(t, uploadedBytesTotal) - before; got != tc.wantBytes {
				t.Errorf("uploaded bytes = %v, want %v", got, tc.wantBytes)
			}
			if counterValue(t, operation)-operationBefore != 1 {
				t.Errorf("expected one operation with outcome %q", outcome)
			}
			if committed != (tc.wantBytes > 0) || putCalled != (tc.beginErr == nil) {
				t.Fatalf("unexpected execution: committed=%v, putCalled=%v", committed, putCalled)
			}
		})
	}
}

func TestDownloadBytes(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tc := range []struct {
		name       string
		method     string
		body       string
		fileSize   int64
		getErr     error
		rowErr     error
		writeLimit int
		writeErr   error
		wantBytes  float64
		wantStatus int
		outcome    string
	}{
		{name: "full GET", method: "GET", body: "payload", fileSize: 7, wantBytes: 7, wantStatus: 200, outcome: "success"},
		{name: "partial writer error", method: "GET", body: "payload", fileSize: 7, writeLimit: 3, writeErr: failure, wantBytes: 3, wantStatus: 200, outcome: "failed"},
		{name: "short write without error", method: "GET", body: "payload", fileSize: 7, writeLimit: 3, wantBytes: 3, wantStatus: 200, outcome: "failed"},
		{name: "short object excludes Content-Length", method: "GET", body: "abc", fileSize: 7, wantBytes: 3, wantStatus: 200, outcome: "failed"},
		{name: "HEAD excluded", method: "HEAD", body: "payload", fileSize: 7, wantStatus: 200, outcome: "success"},
		{name: "S3 JSON error excluded", method: "GET", getErr: failure, wantStatus: 503, outcome: "failed"},
		{name: "missing metadata JSON excluded", method: "GET", rowErr: pgx.ErrNoRows, wantStatus: 404, outcome: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counterValue(t, downloadedBytesTotal)
			operation := operationsTotal.WithLabelValues("download", tc.outcome, strconv.Itoa(tc.wantStatus))
			operationBefore := counterValue(t, operation)
			getCalled := false
			tx := &bytesTestTx{queryRow: func(string, ...any) pgx.Row {
				if tc.rowErr != nil {
					return bytesTestRow{err: tc.rowErr}
				}
				return bytesFileRow(tc.fileSize)
			}}
			api := bytesTestAPI(&bytesTestDB{tx: tx}, &bytesTestStore{
				get: func(ctx context.Context, key string) (*minio.Object, error) {
					getCalled = true
					if tc.getErr != nil {
						return nil, tc.getErr
					}
					return bytesDownloadStore(t, tc.body).Get(ctx, key)
				},
			})
			w := &bytesTestWriter{ResponseRecorder: httptest.NewRecorder(), limit: tc.writeLimit, writeErr: tc.writeErr}
			api.downloadFile(w, bytesTestRequest(tc.method, "/api/files/1/download", nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := counterValue(t, downloadedBytesTotal) - before; got != tc.wantBytes {
				t.Errorf("downloaded bytes = %v, want %v", got, tc.wantBytes)
			}
			wantOperations := float64(0)
			if tc.method == http.MethodGet {
				wantOperations = 1
			}
			if got := counterValue(t, operation) - operationBefore; got != wantOperations {
				t.Errorf("operations with outcome %q = %v, want %v", tc.outcome, got, wantOperations)
			}
			if getCalled != (tc.rowErr == nil) {
				t.Errorf("unexpected Get call: %v", getCalled)
			}
		})
	}
}

func TestByteMetricsRegistration(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fileserver_uploaded_bytes_total", "fileserver_downloaded_bytes_total"} {
		found := false
		for _, family := range families {
			if family.GetName() == name {
				found = true
				if family.GetType() != dto.MetricType_COUNTER || family.GetHelp() == "" || len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
					t.Errorf("%s must be a counter with Help and no labels", name)
				}
			}
		}
		if !found {
			t.Errorf("%s is not registered", name)
		}
	}
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func bytesTestAPI(db database, store objectStore) *API {
	return &API{DB: db, Store: store, Config: config.Config{MaxFileBytes: 1024}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func bytesTestRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.SetPathValue("id", "1")
	return r.WithContext(context.WithValue(r.Context(), userContextKey{}, User{ID: 1}))
}

type bytesTestDB struct {
	database
	tx       pgx.Tx
	beginErr error
}

func (db *bytesTestDB) Begin(context.Context) (pgx.Tx, error) { return db.tx, db.beginErr }
func (db *bytesTestDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return bytesFileRow(0)
}

type bytesTestTx struct {
	pgx.Tx
	queryRow func(string, ...any) pgx.Row
	commit   func() error
}

func (tx *bytesTestTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	return tx.queryRow(query, args...)
}
func (tx *bytesTestTx) Commit(context.Context) error   { return tx.commit() }
func (tx *bytesTestTx) Rollback(context.Context) error { return nil }

type bytesTestRow struct {
	values []any
	err    error
}

func (r bytesTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func bytesFileRow(size int64) pgx.Row {
	return bytesTestRow{values: []any{int64(1), int64(1), "test.txt", "fileserver/test", "text/plain", size, time.Time{}, time.Time{}}}
}

type bytesTestStore struct {
	objectStore
	put func(io.Reader, int64) (int64, error)
	get func(context.Context, string) (*minio.Object, error)
}

func (s *bytesTestStore) Put(_ context.Context, _, _ string, reader io.Reader, hint int64) (int64, error) {
	return s.put(reader, hint)
}
func (s *bytesTestStore) Get(ctx context.Context, key string) (*minio.Object, error) {
	return s.get(ctx, key)
}

type bytesTestWriter struct {
	*httptest.ResponseRecorder
	limit    int
	writeErr error
}

func (w *bytesTestWriter) Write(p []byte) (int, error) {
	if w.limit > 0 && len(p) > w.limit {
		p = p[:w.limit]
	} else if w.writeErr != nil && w.limit == 0 {
		return 0, w.writeErr
	}
	n, _ := w.ResponseRecorder.Write(p)
	return n, w.writeErr
}

type bytesTestTransport func(*http.Request) (*http.Response, error)

func (f bytesTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bytesDownloadStore(t *testing.T, body string) *storage.Store {
	t.Helper()
	client, err := minio.New("s3.example.test", &minio.Options{
		Region: "us-east-1",
		Transport: bytesTestTransport(func(r *http.Request) (*http.Response, error) {
			data := body
			if r.Method == http.MethodHead {
				data = ""
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Length": {strconv.Itoa(len(body))},
					"Content-Type":   {"application/octet-stream"},
					"Last-Modified":  {time.Unix(0, 0).UTC().Format(http.TimeFormat)},
					"Etag":           {`"test-etag"`},
				},
				ContentLength: int64(len(body)),
				Body:          io.NopCloser(strings.NewReader(data)),
				Request:       r,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &storage.Store{Client: client, Bucket: "test-bucket"}
}
