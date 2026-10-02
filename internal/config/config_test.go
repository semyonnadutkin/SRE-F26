package config

import (
	"net/url"
	"testing"
)

func baseEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"DATABASE_URL": "postgres://fileserver:test%3A%40password@postgres:5432/fileserver?sslmode=disable", "POSTGRES_USER": "fileserver", "POSTGRES_PASSWORD": "test:@password", "POSTGRES_DB": "fileserver",
		"APP_ORIGIN": "http://localhost", "COOKIE_SECURE": "false", "FILESERVER_PORT": "8080",
		"S3_ENDPOINT": "http://s3:8333", "S3_ACCESS_KEY": "local", "S3_SECRET_KEY": "secret", "S3_BUCKET": "files", "S3_REGION": "us-east-1",
		"SESSION_TTL": "168h", "MAX_FILE_BYTES": "52428800",
	} {
		t.Setenv(key, value)
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"DATABASE_URL", "invalid"}, {"DATABASE_URL", "https://postgres/files"}, {"DATABASE_URL", "postgres://postgres"}, {"APP_ORIGIN", "http://localhost/"},
		{"APP_ORIGIN", "https://files.example.test"}, {"COOKIE_SECURE", "true"},
		{"S3_ENDPOINT", "http://user:password@s3:8333"}, {"S3_SECRET_KEY", ""},
		{"SESSION_TTL", "0s"}, {"SESSION_TTL", "7d"}, {"MAX_FILE_BYTES", "0"}, {"FILESERVER_PORT", "65536"},
	}
	for _, item := range cases {
		t.Run(item.key+"/"+item.value, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(item.key, item.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLocalDatabaseAndHTTPS(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil || cfg.CookieSecure || cfg.MaxFileBytes != 52428800 {
		t.Fatal("invalid local configuration", err)
	}
	connection, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := connection.User.Password()
	if password != "test:@password" || connection.Host != "postgres:5432" {
		t.Fatal("database credentials were not URL encoded correctly")
	}
	t.Setenv("APP_ORIGIN", "https://files.example.test")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("DATABASE_URL", "postgres://external.example.test/files?sslmode=verify-full")
	external, err := Load()
	if err != nil || !external.CookieSecure || external.DatabaseURL != "postgres://external.example.test/files?sslmode=verify-full" {
		t.Fatal("external configuration failed", err)
	}
}
