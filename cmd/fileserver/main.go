package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/semyonnadutkin/SRE-F26/internal/config"
	"github.com/semyonnadutkin/SRE-F26/internal/httpapi"
	"github.com/semyonnadutkin/SRE-F26/internal/maintenance"
	"github.com/semyonnadutkin/SRE-F26/internal/postgres"
	"github.com/semyonnadutkin/SRE-F26/internal/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("application failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "migrate":
		adminCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return migrate(adminCtx, os.Args[2:], logger)
	case "serve", "bucket-init", "cleanup", "audit":
	default:
		return errors.New("usage: fileserver [serve|migrate|bucket-init|cleanup|audit]")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, err := storage.New(cfg)
	if err != nil {
		return err
	}
	if command == "bucket-init" {
		adminCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return store.Init(adminCtx)
	}

	dbCtx, cancelDB := context.WithTimeout(ctx, 15*time.Second)
	pool, err := postgres.OpenPool(dbCtx, cfg.DatabaseURL)
	cancelDB()
	if err != nil {
		return err
	}
	defer pool.Close()

	if command == "cleanup" || command == "audit" {
		adminCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		if command == "cleanup" {
			return maintenance.Cleanup(adminCtx, pool, store, logger)
		}
		return maintenance.Audit(adminCtx, pool, store, logger)
	}

	api := &httpapi.API{DB: pool, Store: store, Config: cfg, Log: logger}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandler(api),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      11 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("starting HTTP server", "addr", server.Addr)
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down HTTP server")
	}

	// A second signal stops immediately; the first allows in-flight requests to finish.
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	if err := <-serverErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("stop HTTP: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}
