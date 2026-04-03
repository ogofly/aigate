package auth

import (
	"net/http"
	"strings"
)

type Auth struct {
	keys map[string]struct{}
}

func New(keys []string) *Auth {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	return &Auth{keys: set}
}

func (a *Auth) Check(r *http.Request) bool {
	value := r.Header.Get("Authorization")
	if value == "" {
		return false
	}

	token, ok := strings.CutPrefix(value, "Bearer ")
	if !ok || token == "" {
		return false
	}

	_, exists := a.keys[token]
	return exists
}
