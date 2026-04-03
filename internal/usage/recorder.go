package usage

import (
	"sort"
	"sync"
	"time"

	"aigate/internal/auth"
)

type Record struct {
	Timestamp      time.Time     `json:"timestamp"`
	APIKey         string        `json:"api_key"`
	KeyName        string        `json:"key_name,omitempty"`
	Owner          string        `json:"owner,omitempty"`
	Purpose        string        `json:"purpose,omitempty"`
	Endpoint       string        `json:"endpoint"`
	Provider       string        `json:"provider,omitempty"`
	PublicModel    string        `json:"public_model,omitempty"`
	UpstreamModel  string        `json:"upstream_model,omitempty"`
	Success        bool          `json:"success"`
	RequestTokens  int           `json:"request_tokens"`
	ResponseTokens int           `json:"response_tokens"`
	TotalTokens    int           `json:"total_tokens"`
	StatusCode     int           `json:"status_code"`
	Latency        time.Duration `json:"-"`
	LatencyMs      int64         `json:"latency_ms"`
}

type Summary struct {
	APIKey         string `json:"api_key"`
	KeyName        string `json:"key_name,omitempty"`
	Owner          string `json:"owner,omitempty"`
	Purpose        string `json:"purpose,omitempty"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	ErrorCount     int64  `json:"error_count"`
	RequestTokens  int64  `json:"request_tokens"`
	ResponseTokens int64  `json:"response_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
}

type Recorder struct {
	mu         sync.RWMutex
	summaries  map[string]*Summary
	records    []Record
	maxRecords int
}

func New(maxRecords int) *Recorder {
	if maxRecords <= 0 {
		maxRecords = 1000
	}
	return &Recorder{
		summaries:  make(map[string]*Summary),
		maxRecords: maxRecords,
	}
}

func (r *Recorder) Record(record Record) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record.LatencyMs = record.Latency.Milliseconds()
	r.records = append(r.records, record)
	if len(r.records) > r.maxRecords {
		r.records = r.records[len(r.records)-r.maxRecords:]
	}

	summary, ok := r.summaries[record.APIKey]
	if !ok {
		summary = &Summary{
			APIKey:  record.APIKey,
			KeyName: record.KeyName,
			Owner:   record.Owner,
			Purpose: record.Purpose,
		}
		r.summaries[record.APIKey] = summary
	}

	summary.RequestCount++
	if record.Success {
		summary.SuccessCount++
	} else {
		summary.ErrorCount++
	}
	summary.RequestTokens += int64(record.RequestTokens)
	summary.ResponseTokens += int64(record.ResponseTokens)
	summary.TotalTokens += int64(record.TotalTokens)
}

func (r *Recorder) Summaries() []Summary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Summary, 0, len(r.summaries))
	for _, summary := range r.summaries {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIKey < out[j].APIKey
	})
	return out
}

func (r *Recorder) SummaryByKey(key string) (Summary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summary, ok := r.summaries[key]
	if !ok {
		return Summary{}, false
	}
	return *summary, true
}

func NewRecord(principal auth.Principal, endpoint, provider, publicModel, upstreamModel string, success bool, requestTokens, responseTokens, totalTokens, statusCode int, latency time.Duration) Record {
	return Record{
		Timestamp:      time.Now(),
		APIKey:         principal.Key,
		KeyName:        principal.Name,
		Owner:          principal.Owner,
		Purpose:        principal.Purpose,
		Endpoint:       endpoint,
		Provider:       provider,
		PublicModel:    publicModel,
		UpstreamModel:  upstreamModel,
		Success:        success,
		RequestTokens:  requestTokens,
		ResponseTokens: responseTokens,
		TotalTokens:    totalTokens,
		StatusCode:     statusCode,
		Latency:        latency,
	}
}
