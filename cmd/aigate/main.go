package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/usage"
)

func main() {
	configPath := flag.String("config", "configs/aigate.example.json", "path to config file")
	flag.Parse()

	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	providers := make(map[string]provider.Provider, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		p, err := provider.NewOpenAILike(pc)
		if err != nil {
			log.Fatalf("init provider %q: %v", pc.Name, err)
		}
		providers[pc.Name] = p
	}

	rt, err := router.New(providers, cfg.Models)
	if err != nil {
		log.Fatalf("init router: %v", err)
	}

	authenticator := auth.New(cfg.Auth.Keys)
	recorder := usage.New(1000)
	handler := httpapi.New(authenticator, rt, recorder)

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
