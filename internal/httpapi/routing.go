package httpapi

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"llmgate/internal/auth"
	"llmgate/internal/router"
)

func routeAccess(principal auth.Principal) router.Access {
	access := principal.ModelAccess
	if strings.TrimSpace(access) == "" {
		access = "all"
	}
	return router.Access{
		ModelAccess:   access,
		ModelRouteIDs: append([]string(nil), principal.ModelRouteIDs...),
	}
}

func routeProviderOverride(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-Provider"))
}

func routeSessionSeed(r *http.Request, raw map[string]any) string {
	if r != nil {
		if seed := strings.TrimSpace(r.Header.Get("session_id")); seed != "" {
			return "h:session_id:" + seed
		}
		if seed := strings.TrimSpace(r.Header.Get("conversation_id")); seed != "" {
			return "h:conversation_id:" + seed
		}
	}
	if raw != nil {
		if seed, _ := raw["prompt_cache_key"].(string); strings.TrimSpace(seed) != "" {
			return "b:prompt_cache_key:" + strings.TrimSpace(seed)
		}
		data, err := json.Marshal(raw)
		if err == nil && len(data) > 0 {
			sum := md5.Sum(data)
			return "body:" + hex.EncodeToString(sum[:])
		}
	}
	return ""
}

func writeRouteError(w http.ResponseWriter, requestStatus int, err error) {
	switch {
	case errors.Is(err, router.ErrModelNotAllowed):
		writeError(w, http.StatusForbidden, "model_not_allowed", "model not allowed")
	case errors.Is(err, router.ErrModelNotFound):
		writeError(w, requestStatus, "model_not_found", "model not found")
	default:
		writeError(w, requestStatus, "model_not_found", "model not found")
	}
}

func retryableUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	status, ok := parseUpstreamStatus(err.Error())
	if !ok {
		return true
	}
	return retryableStatus(status)
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func parseUpstreamStatus(message string) (int, bool) {
	const marker = "upstream status "
	idx := strings.Index(strings.ToLower(message), marker)
	if idx < 0 {
		return 0, false
	}
	rest := message[idx+len(marker):]
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, false
	}
	status, err := strconv.Atoi(strings.Trim(fields[0], ":"))
	if err != nil {
		return 0, false
	}
	return status, true
}

func maxAttempts(totalRoutes int, configured int) int {
	if configured <= 0 {
		configured = 2
	}
	if totalRoutes < configured {
		return totalRoutes
	}
	return configured
}

func providerNotFoundError(target router.RouteTarget, err error) error {
	return fmt.Errorf("provider %q for route %q not found: %w", target.ProviderName, target.ID, err)
}
