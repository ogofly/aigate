package auth

import (
	"net/http"
	"strings"
	"sync"

	"llmgate/internal/config"
)

type Auth struct {
	mu   sync.RWMutex
	keys map[string]Principal
}

func New(keys []config.KeyConfig) *Auth {
	a := &Auth{}
	a.Update(keys)
	return a
}

func (a *Auth) Update(keys []config.KeyConfig) {
	set := make(map[string]Principal, len(keys))
	for _, key := range keys {
		if key.Key == "" {
			continue
		}
		set[key.Key] = Principal{
			Key:           key.Key,
			Name:          key.Name,
			Owner:         key.Owner,
			Purpose:       key.Purpose,
			ModelAccess:   key.ModelAccess,
			ModelRouteIDs: append([]string(nil), key.ModelRouteIDs...),
		}
	}
	a.mu.Lock()
	a.keys = set
	a.mu.Unlock()
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

	a.mu.RLock()
	defer a.mu.RUnlock()
	principal, exists := a.keys[token]
	return principal, exists
}

func (a *Auth) Check(r *http.Request) bool {
	_, ok := a.Authenticate(r)
	return ok
}
