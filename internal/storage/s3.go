package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/semyonnadutkin/SRE-F26/internal/config"
)

type Store struct {
	Client *minio.Client
	Bucket string
}

func New(cfg config.Config) (*Store, error) {
	endpoint, err := url.Parse(cfg.S3Endpoint)
	if err != nil {
		return nil, errors.New("invalid S3 endpoint")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       cfg.S3Region,
		Transport:    transport,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize S3 client: %w", err)
	}
	return &Store{Client: client, Bucket: cfg.S3Bucket}, nil
}

func (s *Store) Init(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return storageFailure("check bucket", err)
	}
	if exists {
		return nil
	}
	if err := s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{}); err != nil {
		return storageFailure("create bucket", err)
	}
	return nil
}

func (s *Store) Ready(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return storageFailure("check bucket", err)
	}
	if !exists {
		return errors.New("S3 bucket does not exist")
	}
	return nil
}

func (s *Store) Put(
	ctx context.Context,
	key string,
	contentType string,
	reader io.Reader,
	size int64,
) (int64, error) {
	options := minio.PutObjectOptions{
		ContentType: contentType,
	}

	if size < 0 {
		options.PartSize = 5 * 1024 * 1024
		options.NumThreads = 1
		options.ConcurrentStreamParts = false
	}

	info, err := s.Client.PutObject(
		ctx,
		s.Bucket,
		key,
		reader,
		size,
		options,
	)

	return info.Size, err
}

func (s *Store) Get(ctx context.Context, key string) (*minio.Object, error) {
	object, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy; Stat checks for failures before HTTP download headers are sent.
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, err
	}
	return object, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.Client.RemoveObject(ctx, s.Bucket, key, minio.RemoveObjectOptions{})
}

func storageFailure(operation string, err error) error {
	// Provider messages may echo signed request details; retain the operation and error code.
	if code := minio.ToErrorResponse(err).Code; code != "" {
		return fmt.Errorf("S3 %s: %s", operation, code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("S3 %s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("S3 %s failed", operation)
}
