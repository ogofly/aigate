package config

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Key     string `json:"key"`
	Name    string `json:"name"`
	Owner   string `json:"owner,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	Admin   bool   `json:"admin,omitempty"`
}

type ProviderConfig struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	APIKeyRef      string `json:"api_key_ref"`
	TimeoutSeconds int    `json:"timeout"`
}

type ModelConfig struct {
	PublicName   string `json:"public_name"`
	Provider     string `json:"provider"`
	UpstreamName string `json:"upstream_name"`
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

	var cfg Config
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
	for _, key := range c.Auth.Keys {
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

	seenProviders := make(map[string]struct{}, len(c.Providers))
	for _, p := range c.Providers {
		if p.Name == "" {
			return errors.New("provider.name is required")
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q base_url is required", p.Name)
		}
		if p.APIKeyRef == "" {
			return fmt.Errorf("provider %q api_key_ref is required", p.Name)
		}
		if _, ok := seenProviders[p.Name]; ok {
			return fmt.Errorf("duplicate provider %q", p.Name)
		}
		seenProviders[p.Name] = struct{}{}
	}

	seenModels := make(map[string]struct{}, len(c.Models))
	for _, m := range c.Models {
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
		if _, ok := seenModels[m.PublicName]; ok {
			return fmt.Errorf("duplicate model %q", m.PublicName)
		}
		seenModels[m.PublicName] = struct{}{}
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
