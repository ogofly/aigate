package provider

import (
	"net/http"
	"strings"
	"time"
	"aigate/internal/config"
)

func newHTTPClient(timeout time.Duration, stream bool) *http.Client {
	client := &http.Client{}
	if !stream {
		client.Timeout = timeout
	}
	return client
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func anthropicVersion(cfg config.ProviderConfig) string {
	if strings.TrimSpace(cfg.AnthropicVersion) != "" {
		return cfg.AnthropicVersion
	}
	return "2023-06-01"
}

func trimTrailingSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}
