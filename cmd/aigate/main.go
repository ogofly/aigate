package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
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

	sqliteStore, err := store.NewSQLite(cfg.Storage.SQLitePath)
	if err != nil {
		log.Fatalf("init sqlite: %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	if err := sqliteStore.SeedProvidersIfEmpty(ctx, cfg.Providers); err != nil {
		log.Fatalf("seed providers: %v", err)
	}
	if err := sqliteStore.SeedModelsIfEmpty(ctx, cfg.Models); err != nil {
		log.Fatalf("seed models: %v", err)
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(ctx, cfg.Auth.Keys); err != nil {
		log.Fatalf("seed auth keys: %v", err)
	}
	providerConfigs, err := sqliteStore.ListProviders(ctx)
	if err != nil {
		log.Fatalf("load providers: %v", err)
	}
	providerNames := make([]string, 0, len(providerConfigs))
	for _, pc := range providerConfigs {
		providerNames = append(providerNames, pc.Name)
	}
	models, err := sqliteStore.ListModels(ctx)
	if err != nil {
		log.Fatalf("load models: %v", err)
	}
	keyConfigs, err := sqliteStore.ListAuthKeys(ctx)
	if err != nil {
		log.Fatalf("load auth keys: %v", err)
	}

	rt, err := router.New(models)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}

	authenticator := auth.New(keyConfigs)
	recorder := usage.New(1000)
	summaries, err := sqliteStore.UsageSummaries(ctx)
	if err != nil {
		log.Fatalf("load usage summaries: %v", err)
	}
	recorder.SeedSummaries(summaries)

	usage.StartFlushLoop(ctx, recorder, sqliteStore, time.Duration(cfg.Storage.FlushIntervalSeconds)*time.Second)
	handler := httpapi.New(authenticator, cfg.Admin, rt, recorder, sqliteStore, providerNames)

	server := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: handler,
	}

	log.Printf("aigate listening on %s", cfg.Server.Listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}
