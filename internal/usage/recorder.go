package usage

import (
	"sort"
	"sync"
	"time"

	"aigate/internal/auth"
)

type ModelSummary struct {
	Model          string `json:"model"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	ErrorCount     int64  `json:"error_count"`
	RequestTokens  int64  `json:"request_tokens"`
	ResponseTokens int64  `json:"response_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	KeyCount       int64  `json:"key_count"`
}

type Record struct {
	Timestamp      time.Time     `json:"timestamp"`
	KeyID          string        `json:"key_id"`
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
	KeyID          string `json:"key_id"`
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
	pending    map[RollupKey]*Rollup
	records    []Record
	maxRecords int
}

type RollupKey struct {
	BucketStart   time.Time
	KeyID         string
	Endpoint      string
	Provider      string
	PublicModel   string
	UpstreamModel string
}

type Rollup struct {
	BucketStart    time.Time
	KeyID          string
	KeyName        string
	Owner          string
	Purpose        string
	Endpoint       string
	Provider       string
	PublicModel    string
	UpstreamModel  string
	RequestCount   int64
	SuccessCount   int64
	ErrorCount     int64
	RequestTokens  int64
	ResponseTokens int64
	TotalTokens    int64
}

func New(maxRecords int) *Recorder {
	if maxRecords <= 0 {
		maxRecords = 1000
	}
	return &Recorder{
		summaries:  make(map[string]*Summary),
		pending:    make(map[RollupKey]*Rollup),
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

	summary, ok := r.summaries[record.KeyID]
	if !ok {
		summary = &Summary{
			KeyID:   record.KeyID,
			APIKey:  record.APIKey,
			KeyName: record.KeyName,
			Owner:   record.Owner,
			Purpose: record.Purpose,
		}
		r.summaries[record.KeyID] = summary
	}
	if summary.APIKey == "" {
		summary.APIKey = record.APIKey
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

	key := RollupKey{
		BucketStart:   record.Timestamp.UTC().Truncate(time.Hour),
		KeyID:         record.KeyID,
		Endpoint:      record.Endpoint,
		Provider:      record.Provider,
		PublicModel:   record.PublicModel,
		UpstreamModel: record.UpstreamModel,
	}
	rollup, ok := r.pending[key]
	if !ok {
		rollup = &Rollup{
			BucketStart:   key.BucketStart,
			KeyID:         record.KeyID,
			KeyName:       record.KeyName,
			Owner:         record.Owner,
			Purpose:       record.Purpose,
			Endpoint:      record.Endpoint,
			Provider:      record.Provider,
			PublicModel:   record.PublicModel,
			UpstreamModel: record.UpstreamModel,
		}
		r.pending[key] = rollup
	}
	rollup.RequestCount++
	if record.Success {
		rollup.SuccessCount++
	} else {
		rollup.ErrorCount++
	}
	rollup.RequestTokens += int64(record.RequestTokens)
	rollup.ResponseTokens += int64(record.ResponseTokens)
	rollup.TotalTokens += int64(record.TotalTokens)
}

func (r *Recorder) Summaries() []Summary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Summary, 0, len(r.summaries))
	for _, summary := range r.summaries {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].KeyID < out[j].KeyID
	})
	return out
}

func (r *Recorder) SummaryByKey(key string) (Summary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summary, ok := r.summaries[KeyID(key)]
	if !ok {
		return Summary{}, false
	}
	return *summary, true
}

func (r *Recorder) SeedSummaries(summaries []Summary) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, summary := range summaries {
		copySummary := summary
		indexKey := summary.KeyID
		if indexKey == "" {
			indexKey = KeyID(summary.APIKey)
			copySummary.KeyID = indexKey
		}
		r.summaries[indexKey] = &copySummary
	}
}

func (r *Recorder) DrainPending() []Rollup {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Rollup, 0, len(r.pending))
	for _, rollup := range r.pending {
		out = append(out, *rollup)
	}
	r.pending = make(map[RollupKey]*Rollup)
	sort.Slice(out, func(i, j int) bool {
		if out[i].KeyID == out[j].KeyID {
			return out[i].BucketStart.Before(out[j].BucketStart)
		}
		return out[i].KeyID < out[j].KeyID
	})
	return out
}

func (r *Recorder) RestorePending(rollups []Rollup) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, rollup := range rollups {
		key := RollupKey{
			BucketStart:   rollup.BucketStart,
			KeyID:         rollup.KeyID,
			Endpoint:      rollup.Endpoint,
			Provider:      rollup.Provider,
			PublicModel:   rollup.PublicModel,
			UpstreamModel: rollup.UpstreamModel,
		}
		existing, ok := r.pending[key]
		if !ok {
			copyRollup := rollup
			r.pending[key] = &copyRollup
			continue
		}
		existing.RequestCount += rollup.RequestCount
		existing.SuccessCount += rollup.SuccessCount
		existing.ErrorCount += rollup.ErrorCount
		existing.RequestTokens += rollup.RequestTokens
		existing.ResponseTokens += rollup.ResponseTokens
		existing.TotalTokens += rollup.TotalTokens
	}
}

func NewRecord(principal auth.Principal, endpoint, provider, publicModel, upstreamModel string, success bool, requestTokens, responseTokens, totalTokens, statusCode int, latency time.Duration) Record {
	return Record{
		Timestamp:      time.Now(),
		KeyID:          KeyID(principal.Key),
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
