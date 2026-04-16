package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"

	"aigate/internal/auth"
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
	log.Printf("method=%s path=%s op=chat_completions", r.Method, r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		log.Printf("method=%s path=%s op=chat_completions auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if req.Model == "" {
		log.Printf("method=%s path=%s op=chat_completions error=model_required", r.Method, r.URL.Path)
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(req.Model)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s error=model_not_found", r.Method, r.URL.Path, req.Model)
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s upstream_model=%s stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, target.UpstreamModel, req.Stream)
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "provider_not_found", err.Error())
		return
	}

	if req.Stream {
		requestID := nextStreamRequestID()
		streamResp, err := h.client.ChatStream(r.Context(), providerCfg, &req, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=chat_completions request_id=%s model=%s provider=%s error=%v", r.Method, r.URL.Path, requestID, req.Model, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		defer streamResp.Body.Close()
		log.Printf("method=%s path=%s op=chat_completions event=stream_start request_id=%s key_id=%s provider=%s model=%s upstream_model=%s remote_addr=%s user_agent=%q", r.Method, r.URL.Path, requestID, usage.KeyID(principal.Key), target.ProviderName, req.Model, target.UpstreamModel, r.RemoteAddr, r.UserAgent())

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported")
			return
		}

		copyProxyHeaders(w.Header(), streamResp.Header)
		w.WriteHeader(streamResp.StatusCode)

		if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
			bytesSent, copyErr := io.Copy(w, streamResp.Body)
			if copyErr != nil {
				log.Printf("method=%s path=%s op=chat_completions event=stream_abort request_id=%s provider=%s model=%s reason=downstream_write_error err=%v duration_ms=%d bytes_sent=%d upstream_status=%d", r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, copyErr, time.Since(start).Milliseconds(), bytesSent, streamResp.StatusCode)
				return
			}
			h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, streamResp.StatusCode, time.Since(start))
			log.Printf("method=%s path=%s op=chat_completions event=stream_end request_id=%s provider=%s model=%s status=%d duration_ms=%d bytes_sent=%d stream=%t", r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, streamResp.StatusCode, time.Since(start).Milliseconds(), bytesSent, true)
			return
		}

		streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, streamErr := proxyStreamBody(w, flusher, streamResp.Body, r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, start)
		if streamErr != nil {
			return
		}
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, streamResp.StatusCode, time.Since(start))
		log.Printf("method=%s path=%s op=chat_completions event=stream_end request_id=%s provider=%s model=%s status=%d duration_ms=%d chunk_count=%d bytes_sent=%d sse_event_count=%d saw_done=%t usage_present=%t request_tokens=%d response_tokens=%d total_tokens=%d last_events=%q stream=%t", r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, streamResp.StatusCode, time.Since(start).Milliseconds(), chunkCount, bytesSent, sseEventCount, sawDone, streamUsage.present, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, strings.Join(lastEvents, " | "), true)
		return
	}

	resp, err := h.client.Chat(r.Context(), providerCfg, &req, target.UpstreamModel)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, false)
}

func proxyStreamBody(w http.ResponseWriter, flusher http.Flusher, body io.Reader, method, path, requestID, providerName, model string, start time.Time) (streamUsageSnapshot, int, int, int, bool, []string, error) {
	buf := make([]byte, 1024)
	chunkCount := 0
	bytesSent := 0
	// lastChunkAt := start
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
				log.Printf("method=%s path=%s op=chat_completions event=first_chunk request_id=%s provider=%s model=%s ttfb_ms=%d bytes=%d", method, path, requestID, providerName, model, now.Sub(start).Milliseconds(), n)
				firstChunkLogged = true
			}
			// else if chunkCount == 5 || chunkCount%20 == 0 || now.Sub(lastChunkAt) >= 10*time.Second {
			// 	log.Printf("method=%s path=%s op=chat_completions event=stream_progress request_id=%s provider=%s model=%s chunk_count=%d bytes_sent=%d since_start_ms=%d since_last_chunk_ms=%d", method, path, requestID, providerName, model, chunkCount, bytesSent, now.Sub(start).Milliseconds(), now.Sub(lastChunkAt).Milliseconds())
			// }
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				log.Printf("method=%s path=%s op=chat_completions event=stream_abort request_id=%s provider=%s model=%s reason=downstream_write_error err=%v duration_ms=%d chunk_count=%d bytes_sent=%d sse_event_count=%d saw_done=%t last_events=%q", method, path, requestID, providerName, model, writeErr, time.Since(start).Milliseconds(), chunkCount, bytesSent, sseEventCount, sawDone, strings.Join(lastEvents, " | "))
				return streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, writeErr
			}
			flusher.Flush()
			// lastChunkAt = now
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			return streamUsage, chunkCount, bytesSent, sseEventCount, sawDone, lastEvents, nil
		}
		log.Printf("method=%s path=%s op=chat_completions event=stream_abort request_id=%s provider=%s model=%s reason=%s err=%v duration_ms=%d chunk_count=%d bytes_sent=%d sse_event_count=%d saw_done=%t last_events=%q", method, path, requestID, providerName, model, streamAbortReason(readErr), readErr, time.Since(start).Milliseconds(), chunkCount, bytesSent, sseEventCount, sawDone, strings.Join(lastEvents, " | "))
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

	return streamUsageSnapshot{}, false
}
