package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/logger"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

func main() {
	configPath := flag.String("config", "config.example.json", "path to config file")
	flag.Parse()

	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger.Init()

	sqliteStore, err := store.NewSQLite(cfg.Storage.SQLitePath)
	if err != nil {
		slog.Error("init sqlite", "error", err)
		os.Exit(1)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	if err := sqliteStore.SeedProvidersIfEmpty(ctx, cfg.Providers); err != nil {
		slog.Error("seed providers", "error", err)
		os.Exit(1)
	}
	if err := sqliteStore.SeedModelsIfEmpty(ctx, cfg.Models); err != nil {
		slog.Error("seed models", "error", err)
		os.Exit(1)
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(ctx, cfg.Auth.Keys); err != nil {
		slog.Error("seed auth keys", "error", err)
		os.Exit(1)
	}
	providerConfigs, err := sqliteStore.ListProviders(ctx)
	if err != nil {
		slog.Error("load providers", "error", err)
		os.Exit(1)
	}
	providerNames := make([]string, 0, len(providerConfigs))
	for _, pc := range providerConfigs {
		providerNames = append(providerNames, pc.Name)
	}
	models, err := sqliteStore.ListModels(ctx)
	if err != nil {
		slog.Error("load models", "error", err)
		os.Exit(1)
	}
	keyConfigs, err := sqliteStore.ListAuthKeys(ctx)
	if err != nil {
		slog.Error("load auth keys", "error", err)
		os.Exit(1)
	}

	rt, err := router.New(models)
	if err != nil {
		slog.Error("init router", "error", err)
		os.Exit(1)
	}

	authenticator := auth.New(keyConfigs)
	recorder := usage.New(1000)
	summaries, err := sqliteStore.UsageSummaries(ctx)
	if err != nil {
		slog.Error("load usage summaries", "error", err)
		os.Exit(1)
	}
	recorder.SeedSummaries(summaries)

	usage.StartFlushLoop(ctx, recorder, sqliteStore, time.Duration(cfg.Storage.FlushIntervalSeconds)*time.Second)
	handler := httpapi.New(authenticator, cfg.Admin, rt, recorder, sqliteStore, providerNames)

	server := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: handler,
	}

	slog.Info("server starting", "addr", cfg.Server.Listen, "admin", "http://localhost"+adminPort(cfg.Server.Listen)+"/admin")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func adminPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ":" + addr
	}
	return ":" + port
}
