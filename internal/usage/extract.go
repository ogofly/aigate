package usage

func ExtractUsage(payload map[string]any) (requestTokens, responseTokens, totalTokens int) {
	// OpenAI Chat Completions / standard: {"usage": {...}}
	if usage, ok := payload["usage"].(map[string]any); ok {
		return extractTokens(usage)
	}

	// OpenAI Responses API: {"type":"response.completed","response":{"usage":{...}}}
	if resp, ok := payload["response"].(map[string]any); ok {
		if usage, ok := resp["usage"].(map[string]any); ok {
			return extractTokens(usage)
		}
	}

	return 0, 0, 0
}

func extractTokens(rawUsage map[string]any) (requestTokens, responseTokens, totalTokens int) {
	requestTokens = intValue(rawUsage["prompt_tokens"])
	if requestTokens == 0 {
		requestTokens = intValue(rawUsage["input_tokens"])
	}

	responseTokens = intValue(rawUsage["completion_tokens"])
	if responseTokens == 0 {
		responseTokens = intValue(rawUsage["output_tokens"])
	}

	totalTokens = intValue(rawUsage["total_tokens"])
	if totalTokens == 0 {
		totalTokens = requestTokens + responseTokens
	}

	return requestTokens, responseTokens, totalTokens
}

func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}
