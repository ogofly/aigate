package auth

import (
	"net/http"
	"strings"

	"aigate/internal/config"
)

type Auth struct {
	keys map[string]Principal
}

func New(keys []config.KeyConfig) *Auth {
	set := make(map[string]Principal, len(keys))
	for _, key := range keys {
		if key.Key == "" {
			continue
		}
		set[key.Key] = Principal{
			Key:     key.Key,
			Name:    key.Name,
			Owner:   key.Owner,
			Purpose: key.Purpose,
			Admin:   key.Admin,
		}
	}
	return &Auth{keys: set}
}

func (a *Auth) Authenticate(r *http.Request) (Principal, bool) {
	value := r.Header.Get("Authorization")
	if value == "" {
		return Principal{}, false
	}

	token, ok := strings.CutPrefix(value, "Bearer ")
	if !ok || token == "" {
		return Principal{}, false
	}

	principal, exists := a.keys[token]
	return principal, exists
}

func (a *Auth) Check(r *http.Request) bool {
	_, ok := a.Authenticate(r)
	return ok
}
