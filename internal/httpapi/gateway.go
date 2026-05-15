package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"llmgate/internal/auth"
	"llmgate/internal/config"
	"llmgate/internal/logger"
	"llmgate/internal/provider"
	"llmgate/internal/router"
	"llmgate/internal/store"
	"llmgate/internal/usage"
)

type chatCompletionClient interface {
	Chat(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.OpenAIResponse, error)
	ChatStream(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error)
}

type messagesClient interface {
	Messages(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.AnthropicResponse, error)
	MessagesStream(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error)
}

type embeddingsClient interface {
	Embed(ctx context.Context, provider config.ProviderConfig, req provider.EmbeddingRequest, upstreamModel string) (*provider.EmbeddingResponse, error)
}

type responsesClient interface {
	Responses(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.OpenAIResponse, error)
	ResponsesStream(ctx context.Context, provider config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error)
}

type gatewayProviderClient interface {
	chatCompletionClient
	messagesClient
	embeddingsClient
	responsesClient
}

type gatewayRoutePlanner interface {
	ResolvePlan(model string, access router.Access, sessionSeed string) ([]router.RouteTarget, error)
}

type gatewayStore interface {
	GetProvider(ctx context.Context, name string) (config.ProviderConfig, error)
	GetRoutingSettings(ctx context.Context) (config.RoutingConfig, error)
}

type GatewayExecutor struct {
	router   gatewayRoutePlanner
	store    gatewayStore
	client   gatewayProviderClient
	recorder *usage.Recorder
}

func NewGatewayExecutor(rt *router.Router, sqliteStore *store.SQLiteStore, client gatewayProviderClient, recorder *usage.Recorder) *GatewayExecutor {
	return &GatewayExecutor{
		router:   rt,
		store:    sqliteStore,
		client:   client,
		recorder: recorder,
	}
}

type gatewayJSONEndpoint struct {
	op            string
	usageEndpoint string
	model         string
	raw           map[string]any
	call          func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (any, error)
}

type gatewayStreamEndpoint struct {
	op            string
	usageEndpoint string
	model         string
	raw           map[string]any
	call          func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (*provider.StreamResponse, error)
}

type gatewayJSONResult struct {
	status  int
	payload any
}

type gatewayError struct {
	status  int
	code    string
	message string
}

func (e *GatewayExecutor) ExecuteJSON(ctx context.Context, r *http.Request, principal auth.Principal, start time.Time, endpoint gatewayJSONEndpoint) (gatewayJSONResult, *gatewayError) {
	plan, err := e.resolveRoutePlan(r, principal, endpoint.model, endpoint.raw)
	if err != nil {
		logger.L.Warn("model not found", "op", endpoint.op, "model", endpoint.model)
		e.recordUsage(principal, endpoint.usageEndpoint, "", endpoint.model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		return gatewayJSONResult{}, routeGatewayError(http.StatusBadRequest, err)
	}
	target := plan[0]
	logger.L.Info("request resolved", "op", endpoint.op, "model", endpoint.model, "provider", target.ProviderName, "upstream_model", target.UpstreamModel, "stream", false)

	var payload any
	var lastErr error
	attemptLimit := maxAttempts(len(plan), e.routeAttempts(ctx))
	for attempt := 0; attempt < attemptLimit; attempt++ {
		target = plan[attempt]
		providerCfg, err := e.store.GetProvider(ctx, target.ProviderName)
		if err != nil {
			lastErr = providerNotFoundError(target, err)
			break
		}
		payload, err = endpoint.call(ctx, e.client, providerCfg, target)
		if err == nil {
			break
		}
		lastErr = err
		if attempt+1 < attemptLimit && retryableUpstreamError(err) {
			continue
		}
		logger.L.Error("upstream request failed", "op", endpoint.op, "model", endpoint.model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
		e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		return gatewayJSONResult{}, upstreamGatewayError(err)
	}
	if payload == nil {
		e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		return gatewayJSONResult{}, upstreamGatewayError(lastErr)
	}

	requestTokens, responseTokens, totalTokens := extractGatewayUsage(payload)
	e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	logger.L.Info("request complete", "op", endpoint.op, "model", endpoint.model, "provider", target.ProviderName, "client_ip", clientIP(r), "status", http.StatusOK, "request_tokens", requestTokens, "response_tokens", responseTokens, "total_tokens", totalTokens, "duration_ms", time.Since(start).Milliseconds())
	return gatewayJSONResult{status: http.StatusOK, payload: payload}, nil
}

func (e *GatewayExecutor) ExecuteStream(w http.ResponseWriter, r *http.Request, principal auth.Principal, start time.Time, endpoint gatewayStreamEndpoint) bool {
	plan, err := e.resolveRoutePlan(r, principal, endpoint.model, endpoint.raw)
	if err != nil {
		logger.L.Warn("model not found", "op", endpoint.op, "model", endpoint.model)
		e.recordUsage(principal, endpoint.usageEndpoint, "", endpoint.model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeGatewayError(w, routeGatewayError(http.StatusBadRequest, err))
		return false
	}
	target := plan[0]
	logger.L.Info("request resolved", "op", endpoint.op, "model", endpoint.model, "provider", target.ProviderName, "upstream_model", target.UpstreamModel, "stream", true)

	requestID := nextStreamRequestID()
	var streamResp *provider.StreamResponse
	var lastErr error
	attemptLimit := maxAttempts(len(plan), e.routeAttempts(r.Context()))
	for attempt := 0; attempt < attemptLimit; attempt++ {
		target = plan[attempt]
		providerCfg, err := e.store.GetProvider(r.Context(), target.ProviderName)
		if err != nil {
			lastErr = providerNotFoundError(target, err)
			break
		}
		streamResp, err = endpoint.call(r.Context(), e.client, providerCfg, target)
		if err != nil {
			lastErr = err
			if attempt+1 < attemptLimit && retryableUpstreamError(err) {
				continue
			}
			logger.L.Error("stream request failed", "op", endpoint.op, "request_id", requestID, "model", endpoint.model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
			e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			writeGatewayError(w, upstreamGatewayError(err))
			return false
		}
		if streamResp.StatusCode >= 200 && streamResp.StatusCode < 300 {
			break
		}
		if attempt+1 < attemptLimit && retryableStatus(streamResp.StatusCode) {
			_, _ = io.Copy(io.Discard, streamResp.Body)
			streamResp.Body.Close()
			continue
		}
		break
	}
	if streamResp == nil {
		e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeGatewayError(w, upstreamGatewayError(lastErr))
		return false
	}
	defer streamResp.Body.Close()
	logger.L.Info("stream start", "op", endpoint.op, "request_id", requestID, "key_id", usage.KeyID(principal.Key), "provider", target.ProviderName, "model", endpoint.model, "upstream_model", target.UpstreamModel, "client_ip", clientIP(r), "user_agent", r.UserAgent())

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported")
		return false
	}

	copyProxyHeaders(w.Header(), streamResp.Header)
	w.WriteHeader(streamResp.StatusCode)

	if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
		logger.L.Warn("stream error", "op", endpoint.op, "request_id", requestID, "provider", target.ProviderName, "model", endpoint.model, "upstream_status", streamResp.StatusCode, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds())
		bytesSent, copyErr := io.Copy(w, streamResp.Body)
		if copyErr != nil {
			logger.L.Error("stream abort", "op", endpoint.op, "request_id", requestID, "provider", target.ProviderName, "model", endpoint.model, "reason", "downstream_write_error", "error", copyErr, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds(), "bytes_sent", bytesSent, "upstream_status", streamResp.StatusCode)
			return false
		}
		e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, false, 0, 0, 0, streamResp.StatusCode, time.Since(start))
		return true
	}

	streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, streamErr := proxyStreamBody(w, flusher, streamResp.Body, requestID, target.ProviderName, endpoint.model, start)
	if streamErr != nil {
		return false
	}
	e.recordUsage(principal, endpoint.usageEndpoint, target.ProviderName, endpoint.model, target.UpstreamModel, true, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, streamResp.StatusCode, time.Since(start))
	logger.L.Info("stream end", "op", endpoint.op, "request_id", requestID, "provider", target.ProviderName, "model", endpoint.model, "client_ip", clientIP(r), "status", streamResp.StatusCode, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "usage_present", streamUsage.present, "request_tokens", streamUsage.requestTokens, "response_tokens", streamUsage.responseTokens, "total_tokens", streamUsage.totalTokens, "last_event_kinds", strings.Join(lastEvents, " | "))
	return true
}

func (e *GatewayExecutor) resolveRoutePlan(r *http.Request, principal auth.Principal, model string, raw map[string]any) ([]router.RouteTarget, error) {
	access := routeAccess(principal)
	access.Provider = routeProviderOverride(r)
	return e.router.ResolvePlan(model, access, routeSessionSeed(r, raw))
}

func (e *GatewayExecutor) routeAttempts(ctx context.Context) int {
	settings, err := e.store.GetRoutingSettings(ctx)
	if err != nil || !settings.FailoverEnabled {
		return 1
	}
	if settings.FailoverMaxAttempts <= 0 {
		return 2
	}
	return settings.FailoverMaxAttempts
}

func (e *GatewayExecutor) recordUsage(principal auth.Principal, endpoint, providerName, publicModel, upstreamModel string, success bool, requestTokens, responseTokens, totalTokens, statusCode int, latency time.Duration) {
	if e == nil || e.recorder == nil {
		return
	}
	e.recorder.Record(usage.NewRecord(principal, endpoint, providerName, publicModel, upstreamModel, success, requestTokens, responseTokens, totalTokens, statusCode, latency))
}

func extractGatewayUsage(payload any) (int, int, int) {
	switch resp := payload.(type) {
	case *provider.OpenAIResponse:
		return usage.ExtractUsage(map[string]any(*resp))
	case *provider.AnthropicResponse:
		return usage.ExtractUsage(map[string]any(*resp))
	case *provider.EmbeddingResponse:
		return usage.ExtractUsage(map[string]any(*resp))
	default:
		return 0, 0, 0
	}
}

func routeGatewayError(requestStatus int, err error) *gatewayError {
	switch {
	case err == nil:
		return &gatewayError{status: requestStatus, code: "model_not_found", message: "model not found"}
	case errors.Is(err, router.ErrModelNotAllowed):
		return &gatewayError{status: http.StatusForbidden, code: "model_not_allowed", message: "model not allowed"}
	case errors.Is(err, router.ErrModelNotFound):
		return &gatewayError{status: requestStatus, code: "model_not_found", message: "model not found"}
	default:
		return &gatewayError{status: requestStatus, code: "model_not_found", message: "model not found"}
	}
}

func upstreamGatewayError(err error) *gatewayError {
	message := "upstream error"
	if err != nil {
		message = err.Error()
	}
	return &gatewayError{status: http.StatusBadGateway, code: "upstream_error", message: message}
}

func writeGatewayError(w http.ResponseWriter, err *gatewayError) {
	if err == nil {
		return
	}
	writeError(w, err.status, err.code, err.message)
}
