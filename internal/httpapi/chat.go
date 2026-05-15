package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"

	"llmgate/internal/config"
	"llmgate/internal/logger"
	"llmgate/internal/provider"
	"llmgate/internal/router"
	"llmgate/internal/usage"
)

var streamRequestSeq uint64

type streamUsageSnapshot struct {
	requestTokens  int
	responseTokens int
	totalTokens    int
	present        bool
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger.L.Info("request", "op", "chat_completions", "method", r.Method, "path", r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "chat_completions", "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.ChatRequest
	if !decodeJSONBody(w, r, &req, "invalid request body") {
		return
	}

	if req.Model == "" {
		logger.L.Warn("model required", "op", "chat_completions", "method", r.Method, "path", r.URL.Path)
		h.executor.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	if req.Stream {
		h.executor.ExecuteStream(w, r, principal, start, gatewayStreamEndpoint{
			op:            "chat_completions",
			usageEndpoint: "chat.completions",
			model:         req.Model,
			raw:           req.Raw,
			call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (*provider.StreamResponse, error) {
				return client.ChatStream(ctx, providerCfg, &req, target.UpstreamModel)
			},
		})
		return
	}

	result, errResp := h.executor.ExecuteJSON(r.Context(), r, principal, start, gatewayJSONEndpoint{
		op:            "chat_completions",
		usageEndpoint: "chat.completions",
		model:         req.Model,
		raw:           req.Raw,
		call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (any, error) {
			return client.Chat(ctx, providerCfg, &req, target.UpstreamModel)
		},
	})
	if errResp != nil {
		writeGatewayError(w, errResp)
		return
	}
	writeJSON(w, result.status, result.payload)
}

func proxyStreamBody(w http.ResponseWriter, flusher http.Flusher, body io.Reader, requestID, providerName, model string, start time.Time) (streamUsageSnapshot, int, int, int, bool, []string, error) {
	buf := make([]byte, 1024)
	chunkCount := 0
	bytesSent := 0
	firstChunkLogged := false
	sseEventCount := 0
	sawDone := false
	partialLine := ""
	lastEvents := make([]string, 0, 3)
	streamUsage := streamUsageSnapshot{}

	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			now := time.Now()
			chunkCount++
			bytesSent += n
			partialLine, sseEventCount, sawDone, lastEvents, streamUsage = inspectSSEChunk(partialLine, string(buf[:n]), sseEventCount, sawDone, lastEvents, streamUsage)
			if !firstChunkLogged {
				logger.L.Debug("first chunk", "op", "chat_completions", "request_id", requestID, "provider", providerName, "model", model, "ttfb_ms", now.Sub(start).Milliseconds(), "bytes", n)
				firstChunkLogged = true
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				logger.L.Error("stream abort", "op", "chat_completions", "request_id", requestID, "provider", providerName, "model", model, "reason", "downstream_write_error", "error", writeErr, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "last_event_kinds", strings.Join(lastEvents, " | "))
				return streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, writeErr
			}
			flusher.Flush()
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			return streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, nil
		}
		logger.L.Error("stream abort", "op", "chat_completions", "request_id", requestID, "provider", providerName, "model", model, "reason", streamAbortReason(readErr), "error", readErr, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "last_event_kinds", strings.Join(lastEvents, " | "))
		return streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, readErr
	}
}

func copyProxyHeaders(dst, src http.Header) {
	for name, values := range src {
		if !shouldProxyHeader(name) {
			continue
		}
		dst[textproto.CanonicalMIMEHeaderKey(name)] = append([]string(nil), values...)
	}
}

func shouldProxyHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return false
	default:
		return true
	}
}

func nextStreamRequestID() string {
	return fmt.Sprintf("stream-%d", atomic.AddUint64(&streamRequestSeq, 1))
}

func streamAbortReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context canceled"):
		return "context_canceled"
	case strings.Contains(msg, "deadline exceeded"):
		return "deadline_exceeded"
	case strings.Contains(msg, "broken pipe"), strings.Contains(msg, "connection reset by peer"):
		return "client_disconnected"
	case strings.Contains(msg, "unexpected eof"):
		return "unexpected_eof"
	default:
		return "upstream_read_error"
	}
}

func inspectSSEChunk(partial, chunk string, eventCount int, sawDone bool, lastEvents []string, snapshot streamUsageSnapshot) (string, int, bool, []string, streamUsageSnapshot) {
	data := partial + chunk
	lines := strings.Split(data, "\n")
	newPartial := lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		eventCount++
		if payload == "[DONE]" {
			sawDone = true
		} else if usageSnapshot, ok := extractUsageFromSSEPayload(payload); ok {
			snapshot = usageSnapshot
		}
		lastEvents = append(lastEvents, classifySSEPayload(payload))
		if len(lastEvents) > 3 {
			lastEvents = lastEvents[len(lastEvents)-3:]
		}
	}
	return newPartial, eventCount, sawDone, lastEvents, snapshot
}

func classifySSEPayload(payload string) string {
	if payload == "[DONE]" {
		return payload
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return "data"
	}
	if typ, _ := raw["type"].(string); typ != "" {
		return typ
	}
	if _, ok := raw["usage"]; ok {
		return "usage"
	}
	if _, ok := raw["choices"]; ok {
		return "choices"
	}
	return "data"
}

func extractUsageFromSSEPayload(payload string) (streamUsageSnapshot, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return streamUsageSnapshot{}, false
	}

	// OpenAI-compatible: {"usage": {...}, ...}
	if _, ok := raw["usage"].(map[string]any); ok {
		requestTokens, responseTokens, totalTokens := usage.ExtractUsage(raw)
		return streamUsageSnapshot{
			requestTokens:  requestTokens,
			responseTokens: responseTokens,
			totalTokens:    totalTokens,
			present:        true,
		}, true
	}

	// Anthropic: {"message": {"usage": {...}, ...}, "type": "message_start"}
	if msg, ok := raw["message"].(map[string]any); ok {
		if _, ok := msg["usage"].(map[string]any); ok {
			requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any{"usage": msg["usage"]})
			return streamUsageSnapshot{
				requestTokens:  requestTokens,
				responseTokens: responseTokens,
				totalTokens:    totalTokens,
				present:        true,
			}, true
		}
	}

	// OpenAI Responses API: {"type":"response.completed","response":{"usage":{...}}}
	if resp, ok := raw["response"].(map[string]any); ok {
		if _, ok := resp["usage"].(map[string]any); ok {
			requestTokens, responseTokens, totalTokens := usage.ExtractUsage(raw)
			return streamUsageSnapshot{
				requestTokens:  requestTokens,
				responseTokens: responseTokens,
				totalTokens:    totalTokens,
				present:        true,
			}, true
		}
	}

	return streamUsageSnapshot{}, false
}
