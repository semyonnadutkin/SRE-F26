package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/semyonnadutkin/SRE-F26/internal/config"
	"github.com/semyonnadutkin/SRE-F26/migrations"
)

func migrate(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: fileserver migrate up|up-to version|status|down|create name")
	}
	goose.SetLogger(gooseLogger{logger})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if args[0] == "create" {
		if len(args) != 2 || !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(args[1]) {
			return errors.New("usage: migrate create lowercase_name")
		}
		return goose.Create(nil, "migrations", args[1], "sql")
	}
	switch args[0] {
	case "up", "status", "down":
		if len(args) != 1 {
			return errors.New("unexpected migration arguments")
		}
	case "up-to":
		if len(args) != 2 {
			return errors.New("usage: migrate up-to version")
		}
		if version, err := strconv.ParseInt(args[1], 10, 64); err != nil || version < 0 {
			return errors.New("invalid migration version")
		}
	default:
		return errors.New("unknown migration command")
	}

	databaseURL, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		// Omit the underlying error because it may contain credentials.
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	if err := db.PingContext(ctx); err != nil {
		// Omit the connection error because it may contain credentials.
		return errors.New("migration database unavailable")
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.RunContext(ctx, args[0], db, ".", args[1:]...); err != nil {
		return fmt.Errorf("migrate %s: %w", args[0], err)
	}
	return nil
}

type gooseLogger struct {
	logger *slog.Logger
}

func (logger gooseLogger) Printf(format string, args ...any) {
	logger.logger.Info(fmt.Sprintf(format, args...))
}

func (logger gooseLogger) Fatalf(format string, args ...any) {
	logger.logger.Error("migration fatal error")
	os.Exit(1)
}
