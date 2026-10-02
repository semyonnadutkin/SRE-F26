package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr     string
	DatabaseURL  string
	AppOrigin    string
	CookieSecure bool
	SessionTTL   time.Duration
	MaxFileBytes int64
	S3Endpoint   string
	S3Region     string
	S3Bucket     string
	S3AccessKey  string
	S3SecretKey  string
}

func Load() (Config, error) {
	cfg := Config{
		AppOrigin:   env("APP_ORIGIN", "http://localhost"),
		S3Endpoint:  env("S3_ENDPOINT", "http://s3:8333"),
		S3Region:    env("S3_REGION", "us-east-1"),
		S3Bucket:    env("S3_BUCKET", "files"),
		S3AccessKey: os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("S3_SECRET_KEY"),
	}

	databaseURL, err := DatabaseURL()
	if err != nil {
		return cfg, err
	}
	cfg.DatabaseURL = databaseURL

	port := env("FILESERVER_PORT", "8080")
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return cfg, errors.New("invalid FILESERVER_PORT")
	}
	cfg.HTTPAddr = net.JoinHostPort("", port)

	origin, err := url.Parse(cfg.AppOrigin)
	if err != nil || !validOrigin(origin) {
		return cfg, errors.New("APP_ORIGIN must be an HTTP(S) origin without a trailing slash")
	}
	cfg.CookieSecure, err = strconv.ParseBool(env("COOKIE_SECURE", "true"))
	if err != nil {
		return cfg, errors.New("invalid COOKIE_SECURE")
	}
	if cfg.CookieSecure != (origin.Scheme == "https") {
		return cfg, errors.New("COOKIE_SECURE must be true for HTTPS and explicitly false for local HTTP")
	}

	endpoint, err := url.Parse(cfg.S3Endpoint)
	if err != nil || !validOrigin(endpoint) {
		return cfg, errors.New("S3_ENDPOINT must be an HTTP(S) origin")
	}
	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" || cfg.S3Bucket == "" || cfg.S3Region == "" {
		return cfg, errors.New("S3 credentials, bucket, and region are required")
	}

	cfg.SessionTTL, err = time.ParseDuration(env("SESSION_TTL", "168h"))
	if err != nil || cfg.SessionTTL < time.Second || cfg.SessionTTL > 30*24*time.Hour {
		return cfg, errors.New("SESSION_TTL must be between 1s and 720h")
	}
	cfg.MaxFileBytes, err = strconv.ParseInt(env("MAX_FILE_BYTES", "52428800"), 10, 64)
	if err != nil || cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > 5*1024*1024*1024 {
		return cfg, errors.New("MAX_FILE_BYTES must be between 1 and 5368709120")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func validOrigin(value *url.URL) bool {
	return value != nil && value.Host != "" && (value.Scheme == "http" || value.Scheme == "https") &&
		value.User == nil && value.Path == "" && value.RawQuery == "" && value.Fragment == ""
}

func DatabaseURL() (string, error) {
	connection := os.Getenv("DATABASE_URL")
	if connection == "" {
		return "", errors.New("DATABASE_URL is required; run scripts/setup_env.py to populate .env")
	}
	parsed, err := url.Parse(connection)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Path == "" {
		return "", errors.New("DATABASE_URL must be a PostgreSQL connection URL")
	}
	return connection, nil
}
