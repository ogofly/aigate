package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"

	"aigate/internal/auth"
	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/usage"
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if req.Model == "" {
		logger.L.Warn("model required", "op", "chat_completions", "method", r.Method, "path", r.URL.Path)
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	plan, err := h.resolveRoutePlan(r, principal, req.Model, req.Raw)
	if err != nil {
		logger.L.Warn("model not found", "op", "chat_completions", "model", req.Model)
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeRouteError(w, http.StatusBadRequest, err)
		return
	}
	target := plan[0]
	logger.L.Info("request resolved", "op", "chat_completions", "model", req.Model, "provider", target.ProviderName, "upstream_model", target.UpstreamModel, "stream", req.Stream)

	if req.Stream {
		requestID := nextStreamRequestID()
		var streamResp *provider.StreamResponse
		var lastErr error
		attemptLimit := maxAttempts(len(plan), h.routeAttempts(r.Context()))
		for attempt := 0; attempt < attemptLimit; attempt++ {
			target = plan[attempt]
			providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
			if err != nil {
				lastErr = providerNotFoundError(target, err)
				break
			}
			streamResp, err = h.client.ChatStream(r.Context(), providerCfg, &req, target.UpstreamModel)
			if err != nil {
				lastErr = err
				if attempt+1 < attemptLimit && retryableUpstreamError(err) {
					continue
				}
				logger.L.Error("stream request failed", "op", "chat_completions", "request_id", requestID, "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
				h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
				writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
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
			h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			writeError(w, http.StatusBadGateway, "upstream_error", lastErr.Error())
			return
		}
		defer streamResp.Body.Close()
		logger.L.Info("stream start", "op", "chat_completions", "request_id", requestID, "key_id", usage.KeyID(principal.Key), "provider", target.ProviderName, "model", req.Model, "upstream_model", target.UpstreamModel, "client_ip", clientIP(r), "user_agent", r.UserAgent())

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported")
			return
		}

		copyProxyHeaders(w.Header(), streamResp.Header)
		w.WriteHeader(streamResp.StatusCode)

		if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
			logger.L.Warn("stream error", "op", "chat_completions", "request_id", requestID, "provider", target.ProviderName, "model", req.Model, "upstream_status", streamResp.StatusCode, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds())
			bytesSent, copyErr := io.Copy(w, streamResp.Body)
			if copyErr != nil {
				logger.L.Error("stream abort", "op", "chat_completions", "request_id", requestID, "provider", target.ProviderName, "model", req.Model, "reason", "downstream_write_error", "error", copyErr, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds(), "bytes_sent", bytesSent, "upstream_status", streamResp.StatusCode)
				return
			}
			h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, streamResp.StatusCode, time.Since(start))
			return
		}

		streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, streamErr := proxyStreamBody(w, flusher, streamResp.Body, requestID, target.ProviderName, req.Model, start)
		if streamErr != nil {
			return
		}
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, streamResp.StatusCode, time.Since(start))
		logger.L.Info("stream end", "op", "chat_completions", "request_id", requestID, "provider", target.ProviderName, "model", req.Model, "client_ip", clientIP(r), "status", streamResp.StatusCode, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "usage_present", streamUsage.present, "request_tokens", streamUsage.requestTokens, "response_tokens", streamUsage.responseTokens, "total_tokens", streamUsage.totalTokens, "last_events", strings.Join(lastEvents, " | "))
		return
	}

	var resp *provider.OpenAIResponse
	var lastErr error
	attemptLimit := maxAttempts(len(plan), h.routeAttempts(r.Context()))
	for attempt := 0; attempt < attemptLimit; attempt++ {
		target = plan[attempt]
		providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
		if err != nil {
			lastErr = providerNotFoundError(target, err)
			break
		}
		resp, err = h.client.Chat(r.Context(), providerCfg, &req, target.UpstreamModel)
		if err == nil {
			break
		}
		lastErr = err
		if attempt+1 < attemptLimit && retryableUpstreamError(err) {
			continue
		}
		logger.L.Error("chat request failed", "op", "chat_completions", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if resp == nil {
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", lastErr.Error())
		return
	}

	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	logger.L.Info("chat complete", "op", "chat_completions", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "status", http.StatusOK, "request_tokens", requestTokens, "response_tokens", responseTokens, "total_tokens", totalTokens, "duration_ms", time.Since(start).Milliseconds())
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
				logger.L.Error("stream abort", "op", "chat_completions", "request_id", requestID, "provider", providerName, "model", model, "reason", "downstream_write_error", "error", writeErr, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "last_events", strings.Join(lastEvents, " | "))
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
		logger.L.Error("stream abort", "op", "chat_completions", "request_id", requestID, "provider", providerName, "model", model, "reason", streamAbortReason(readErr), "error", readErr, "duration_ms", time.Since(start).Milliseconds(), "chunk_count", chunkCount, "bytes_sent", bytesSent, "sse_event_count", sseEventCount, "saw_done", sawDone, "last_events", strings.Join(lastEvents, " | "))
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

func (h *Handler) recordUsage(principal auth.Principal, endpoint, providerName, publicModel, upstreamModel string, success bool, requestTokens, responseTokens, totalTokens, statusCode int, latency time.Duration) {
	if h.usage == nil {
		return
	}
	h.usage.Record(usage.NewRecord(principal, endpoint, providerName, publicModel, upstreamModel, success, requestTokens, responseTokens, totalTokens, statusCode, latency))
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
		lastEvents = append(lastEvents, summarizeSSEPayload(payload))
		if len(lastEvents) > 3 {
			lastEvents = lastEvents[len(lastEvents)-3:]
		}
	}
	return newPartial, eventCount, sawDone, lastEvents, snapshot
}

func summarizeSSEPayload(payload string) string {
	if payload == "[DONE]" {
		return payload
	}
	if len(payload) <= 96 {
		return payload
	}
	return payload[:93] + "..."
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
