package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"llmgate/internal/config"
)

const (
	maxUpstreamResponseBytes int64 = 32 << 20
	maxUpstreamErrorBytes    int64 = 1 << 20
)

func newHTTPClient(timeout time.Duration, stream bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if timeout > 0 {
		transport.ResponseHeaderTimeout = timeout
	}
	if stream && timeout > 0 {
		dialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return idleTimeoutConn{Conn: conn, timeout: timeout}, nil
		}
	}

	client := &http.Client{Transport: transport}
	if !stream {
		client.Timeout = timeout
	}
	return client
}

type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c idleTimeoutConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(p)
}

func readUpstreamResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = readLimitedBody(resp.Body, maxUpstreamErrorBytes)
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	body, err := readLimitedBody(resp.Body, maxUpstreamResponseBytes)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: limit + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", limit)
	}
	return data, nil
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

func validateAbsoluteHTTPURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("must include a host")
	}
	return nil
}

type resolvedProviderConfig struct {
	baseURL string
	apiKey  string
	timeout time.Duration
}

func resolveProviderConfig(cfg config.ProviderConfig, base string) (resolvedProviderConfig, error) {
	baseURL := trimTrailingSlash(base)
	if err := validateAbsoluteHTTPURL(baseURL); err != nil {
		return resolvedProviderConfig{}, fmt.Errorf("invalid base_url: %w", err)
	}

	timeout := 60 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	apiKey := trimSpace(cfg.APIKey)
	if apiKey == "" && cfg.APIKeyRef != "" {
		apiKey = trimSpace(os.Getenv(cfg.APIKeyRef))
		if apiKey == "" {
			return resolvedProviderConfig{}, fmt.Errorf("provider %q api key env %q is empty", cfg.Name, cfg.APIKeyRef)
		}
	}
	if apiKey == "" {
		return resolvedProviderConfig{}, fmt.Errorf("provider %q requires api_key or api_key_ref", cfg.Name)
	}

	return resolvedProviderConfig{
		baseURL: baseURL,
		apiKey:  apiKey,
		timeout: timeout,
	}, nil
}
