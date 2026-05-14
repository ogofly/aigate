package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Server    ServerConfig     `json:"server"`
	Auth      AuthConfig       `json:"auth"`
	Admin     AdminConfig      `json:"admin"`
	Providers []ProviderConfig `json:"providers"`
	Models    []ModelConfig    `json:"models"`
	Routing   RoutingConfig    `json:"routing"`
	Storage   StorageConfig    `json:"storage"`
}

type ServerConfig struct {
	Listen string `json:"listen"`
}

type AdminConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AuthConfig struct {
	Keys []KeyConfig `json:"keys"`
}

type KeyConfig struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Owner         string   `json:"owner,omitempty"`
	Purpose       string   `json:"purpose,omitempty"`
	ModelAccess   string   `json:"model_access,omitempty"`
	ModelRouteIDs []string `json:"model_route_ids,omitempty"`
}

type ProviderConfig struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	AnthropicVersion string `json:"anthropic_version"`
	APIKey           string `json:"api_key"`
	APIKeyRef        string `json:"api_key_ref"`
	TimeoutSeconds   int    `json:"timeout"`
	Enabled          bool   `json:"enabled"`
}

type ModelConfig struct {
	ID           string `json:"id"`
	PublicName   string `json:"public_name"`
	Provider     string `json:"provider"`
	UpstreamName string `json:"upstream_name"`
	Priority     int    `json:"priority"`
	Weight       int    `json:"weight"`
	Enabled      bool   `json:"enabled"`
}

type RoutingConfig struct {
	Selection           string `json:"selection"`
	FailoverEnabled     bool   `json:"failover_enabled"`
	FailoverMaxAttempts int    `json:"failover_max_attempts"`
}

type StorageConfig struct {
	SQLitePath           string `json:"sqlite_path"`
	FlushIntervalSeconds int    `json:"flush_interval"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := Config{Routing: RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2}}
	if err := json.Unmarshal([]byte(expandEnv(string(data))), &cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	cfg.resolvePaths(path)

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if c.Admin.Username == "" {
		return errors.New("admin.username is required")
	}
	if c.Admin.Password == "" {
		return errors.New("admin.password is required")
	}
	seenKeys := make(map[string]struct{}, len(c.Auth.Keys))
	for i, key := range c.Auth.Keys {
		c.Auth.Keys[i].SetDefaults()
		key = c.Auth.Keys[i]
		if key.Key == "" {
			return errors.New("auth.keys[].key is required")
		}
		if _, ok := seenKeys[key.Key]; ok {
			return fmt.Errorf("duplicate auth key %q", key.Key)
		}
		seenKeys[key.Key] = struct{}{}
	}
	if c.Storage.SQLitePath == "" {
		return errors.New("storage.sqlite_path is required")
	}
	if c.Storage.FlushIntervalSeconds <= 0 {
		c.Storage.FlushIntervalSeconds = 60
	}
	c.Routing.SetDefaults()

	seenProviders := make(map[string]struct{}, len(c.Providers))
	for i, p := range c.Providers {
		c.Providers[i].SetDefaults()
		p = c.Providers[i]
		if err := p.Validate(); err != nil {
			return err
		}
		if _, ok := seenProviders[p.Name]; ok {
			return fmt.Errorf("duplicate provider %q", p.Name)
		}
		seenProviders[p.Name] = struct{}{}
	}

	seenModels := make(map[string]struct{}, len(c.Models))
	for i, m := range c.Models {
		c.Models[i].SetDefaults()
		m = c.Models[i]
		if m.PublicName == "" {
			return errors.New("model.public_name is required")
		}
		if m.Provider == "" {
			return fmt.Errorf("model %q provider is required", m.PublicName)
		}
		if m.UpstreamName == "" {
			return fmt.Errorf("model %q upstream_name is required", m.PublicName)
		}
		if _, ok := seenProviders[m.Provider]; !ok {
			return fmt.Errorf("model %q references unknown provider %q", m.PublicName, m.Provider)
		}
		routeKey := strings.Join([]string{m.PublicName, m.Provider, m.UpstreamName}, "\x00")
		if _, ok := seenModels[routeKey]; ok {
			return fmt.Errorf("duplicate model route %q/%q/%q", m.PublicName, m.Provider, m.UpstreamName)
		}
		seenModels[routeKey] = struct{}{}
	}

	return nil
}

func (k *KeyConfig) SetDefaults() {
	if strings.TrimSpace(k.ModelAccess) == "" {
		k.ModelAccess = "all"
	}
	if k.ModelAccess != "all" && k.ModelAccess != "selected" {
		k.ModelAccess = "all"
	}
}

func (p *ProviderConfig) SetDefaults() {
}

func (m *ModelConfig) SetDefaults() {
	if m.Weight <= 0 {
		m.Weight = 1
	}
}

func (r *RoutingConfig) SetDefaults() {
	switch strings.TrimSpace(r.Selection) {
	case "", "priority", "weight", "random":
		if strings.TrimSpace(r.Selection) == "" {
			r.Selection = "priority"
		}
	default:
		r.Selection = "priority"
	}
	if r.FailoverMaxAttempts <= 0 {
		r.FailoverMaxAttempts = 2
	}
}

func (p ProviderConfig) Validate() error {
	if p.Name == "" {
		return errors.New("provider.name is required")
	}
	if p.BaseURL == "" {
		return fmt.Errorf("provider %q base_url is required", p.Name)
	}
	if err := validateHTTPURL("base_url", p.Name, p.BaseURL); err != nil {
		return err
	}
	if p.AnthropicBaseURL != "" {
		if err := validateHTTPURL("anthropic_base_url", p.Name, p.AnthropicBaseURL); err != nil {
			return err
		}
	}
	if p.APIKey == "" && p.APIKeyRef == "" {
		return fmt.Errorf("provider %q requires api_key or api_key_ref", p.Name)
	}
	if p.TimeoutSeconds < 0 {
		return fmt.Errorf("provider %q timeout must be greater than or equal to 0", p.Name)
	}
	return nil
}

func validateHTTPURL(field, providerName, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("provider %q invalid %s: %w", providerName, field, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("provider %q %s must use http or https", providerName, field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("provider %q %s must include a host", providerName, field)
	}
	return nil
}

func expandEnv(input string) string {
	return os.Expand(input, func(name string) string {
		return strings.TrimSpace(os.Getenv(name))
	})
}

func (c *Config) resolvePaths(configPath string) {
	if c.Storage.SQLitePath == "" || filepath.IsAbs(c.Storage.SQLitePath) {
		return
	}
	configDir := filepath.Dir(configPath)
	c.Storage.SQLitePath = filepath.Clean(filepath.Join(configDir, c.Storage.SQLitePath))
}

func (k *KeyConfig) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err == nil {
		k.Key = raw
		return nil
	}

	type alias KeyConfig
	var out alias
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*k = KeyConfig(out)
	return nil
}

func (p *ProviderConfig) UnmarshalJSON(data []byte) error {
	type alias ProviderConfig
	out := alias{Enabled: true}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*p = ProviderConfig(out)
	return nil
}

func (m *ModelConfig) UnmarshalJSON(data []byte) error {
	type alias ModelConfig
	out := alias{Enabled: true, Weight: 1}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*m = ModelConfig(out)
	return nil
}

func (r *RoutingConfig) UnmarshalJSON(data []byte) error {
	type alias RoutingConfig
	out := alias{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*r = RoutingConfig(out)
	return nil
}
