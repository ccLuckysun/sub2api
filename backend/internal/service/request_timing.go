package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
)

const (
	RequestTimingMethod    = "legacy_difference_v1"
	RequestTimingRowBudget = 200000
	RequestTimingLargeBody = 5 * 1024 * 1024
)

// RequestTiming is an admin-only projection. All missing observations stay null.
type RequestTiming struct {
	GatewayLatencyMs *int     `json:"gateway_latency_ms"`
	BodyBytes        *int64   `json:"body_bytes"`
	DurationMs       *int     `json:"duration_ms"`
	FirstTokenMs     *int     `json:"first_token_ms"`
	UploadMs         *int64   `json:"upload_ms"`
	UpstreamTTFTMs   *int64   `json:"upstream_ttft_ms"`
	StreamMs         *int64   `json:"stream_ms"`
	RealTTFBMs       *int64   `json:"real_ttfb_ms"`
	UploadKBPS       *float64 `json:"upload_kbps"`
	BytesPerToken    *float64 `json:"bytes_per_token"`
	TPSTotal         *float64 `json:"tps_total"`
	TPSStream        *float64 `json:"tps_stream"`
	RawUploadMs      *int64   `json:"raw_upload_ms"`
	ResidualMs       *int64   `json:"timing_residual_ms"`
	ContextTokens    int64    `json:"ctx_tokens"`
	Status           string   `json:"timing_status"`
	Method           string   `json:"timing_method"`
	Reason           *string  `json:"reason"`
	HTTPStream       bool     `json:"is_http_stream"`
	Complete         bool     `json:"is_complete_timing"`
	ValidUpload      bool     `json:"is_valid_upload"`
	ValidTTFT        bool     `json:"is_valid_ttft"`
	ValidReal        bool     `json:"is_valid_real"`
	ValidRate        bool     `json:"is_valid_rate"`
	ValidBody        bool     `json:"is_valid_body"`
}

func DeriveRequestTiming(log *UsageLog) RequestTiming {
	t := RequestTiming{Method: RequestTimingMethod, Status: "unavailable"}
	if log == nil {
		return t
	}
	t.GatewayLatencyMs, t.BodyBytes, t.DurationMs, t.FirstTokenMs = log.GatewayLatencyMs, log.BodyBytes, log.DurationMs, log.FirstTokenMs
	t.ContextTokens = int64(log.InputTokens) + int64(log.CacheReadTokens)
	requestType := log.RequestType
	if requestType == RequestTypeUnknown && log.GatewayLatencyMs != nil {
		requestType = RequestTypeFromLegacy(log.Stream, log.OpenAIWSMode)
	}
	t.HTTPStream = log.Stream && !log.OpenAIWSMode && requestType == RequestTypeStream
	if log.BodyBytes != nil && *log.BodyBytes >= 0 && t.ContextTokens > 0 {
		v := float64(*log.BodyBytes) / float64(t.ContextTokens)
		t.BytesPerToken = &v
	}
	text := log.ImageCount == 0 && log.ImageOutputTokens == 0 && log.VideoCount == 0 && (log.BillingMode == nil || *log.BillingMode == "token")
	if text && log.OutputTokens > 0 && log.DurationMs != nil && *log.DurationMs > 0 {
		v := float64(log.OutputTokens) * 1000 / float64(*log.DurationMs)
		t.TPSTotal = &v
	}
	reason := func(status, why string) { t.Status = status; t.Reason = &why }
	// Keep the same diagnostic projections as v_request_timing. Invalid samples
	// retain their raw inputs and diagnostics, but every validity flag is false.
	if t.HTTPStream {
		if log.FirstTokenMs != nil && *log.FirstTokenMs >= 0 {
			v := int64(*log.FirstTokenMs)
			t.UpstreamTTFTMs = &v
		}
		if log.DurationMs != nil && *log.DurationMs >= 0 && t.UpstreamTTFTMs != nil {
			v := int64(*log.DurationMs) - *t.UpstreamTTFTMs
			if v < 0 {
				v = 0
			}
			t.StreamMs = &v
		}
		if log.GatewayLatencyMs != nil && *log.GatewayLatencyMs >= 0 && log.DurationMs != nil && *log.DurationMs >= 0 {
			raw := int64(*log.GatewayLatencyMs) - int64(*log.DurationMs)
			t.RawUploadMs = &raw
			v := raw
			if v < 0 {
				v = 0
			}
			t.UploadMs = &v
			if t.UpstreamTTFTMs != nil {
				real := v + *t.UpstreamTTFTMs
				t.RealTTFBMs = &real
			}
			if t.StreamMs != nil && t.RealTTFBMs != nil {
				residual := *t.RealTTFBMs + *t.StreamMs - int64(*log.GatewayLatencyMs)
				t.ResidualMs = &residual
			}
		}
	}
	if (log.GatewayLatencyMs != nil && *log.GatewayLatencyMs < 0) || (log.DurationMs != nil && *log.DurationMs < 0) || (log.FirstTokenMs != nil && *log.FirstTokenMs < 0) || (log.BodyBytes != nil && *log.BodyBytes < 0) {
		reason("invalid", "negative_observation")
		return t
	}
	if !t.HTTPStream {
		reason("unavailable", "unsupported_protocol")
		return t
	}
	if (log.DurationMs != nil && log.FirstTokenMs != nil && *log.FirstTokenMs > *log.DurationMs) || (t.RawUploadMs != nil && *t.RawUploadMs < -300) {
		reason("invalid", "inconsistent_observation")
		return t
	}
	if log.GatewayLatencyMs == nil || log.DurationMs == nil {
		reason("unavailable", "missing_total_or_duration")
		return t
	}
	t.ValidUpload = t.RawUploadMs != nil && *t.RawUploadMs >= 0
	t.ValidTTFT = t.ValidUpload && log.FirstTokenMs != nil
	t.ValidReal = t.ValidTTFT
	t.ValidBody = log.BodyBytes != nil
	if t.ValidUpload && *t.UploadMs > 0 && t.ValidBody {
		v := float64(*log.BodyBytes) * 1000 / 1024 / float64(*t.UploadMs)
		t.UploadKBPS = &v
		t.ValidRate = true
	}
	if text && t.ValidTTFT && t.StreamMs != nil && *t.StreamMs > 0 && log.OutputTokens > 0 {
		v := float64(log.OutputTokens) * 1000 / float64(*t.StreamMs)
		t.TPSStream = &v
	}
	if *t.RawUploadMs < 0 {
		reason("partial", "clamped_total")
		return t
	}
	if log.FirstTokenMs == nil {
		reason("partial", "missing_first_token")
		return t
	}
	if log.BodyBytes == nil {
		reason("partial", "missing_body_bytes")
		return t
	}
	t.Status = "complete"
	t.Complete = true
	return t
}

type RequestTimingDetail struct {
	ID                int64         `json:"id"`
	CreatedAt         time.Time     `json:"created_at"`
	AccountID         int64         `json:"account_id"`
	GroupID           *int64        `json:"group_id"`
	Model             string        `json:"model"`
	RequestID         string        `json:"request_id"`
	ClientRequestID   *string       `json:"client_request_id"`
	GatewayRequestID  *string       `json:"gateway_request_id"`
	UpstreamRequestID *string       `json:"upstream_request_id"`
	Stream            bool          `json:"stream"`
	RequestType       string        `json:"request_type"`
	Timing            RequestTiming `json:"timing"`
}

func RequestTimingDetailFromUsage(log *UsageLog) *RequestTimingDetail {
	if log == nil {
		return nil
	}
	d := &RequestTimingDetail{ID: log.ID, CreatedAt: log.CreatedAt, AccountID: log.AccountID, GroupID: log.GroupID, Model: log.Model, RequestID: log.RequestID, UpstreamRequestID: log.UpstreamRequestID, Stream: log.Stream, RequestType: log.RequestType.String(), Timing: DeriveRequestTiming(log)}
	if strings.HasPrefix(log.RequestID, "client:") && len(log.RequestID) > 7 {
		v := strings.TrimPrefix(log.RequestID, "client:")
		d.ClientRequestID = &v
	}
	return d
}

type TimingQuality struct {
	CandidateCount int64    `json:"candidate_count"`
	CompleteCount  int64    `json:"complete_count"`
	InvalidCount   int64    `json:"invalid_count"`
	UploadCount    int64    `json:"upload_count"`
	TTFTCount      int64    `json:"ttft_count"`
	RealCount      int64    `json:"real_count"`
	RateCount      int64    `json:"rate_count"`
	KnownBodyCount int64    `json:"known_body_count"`
	LargeBodyCount int64    `json:"large_body_count"`
	CoverageRatio  *float64 `json:"coverage_ratio"`
}

func (q *TimingQuality) Add(other TimingQuality) {
	q.CandidateCount += other.CandidateCount
	q.CompleteCount += other.CompleteCount
	q.InvalidCount += other.InvalidCount
	q.UploadCount += other.UploadCount
	q.TTFTCount += other.TTFTCount
	q.RealCount += other.RealCount
	q.RateCount += other.RateCount
	q.KnownBodyCount += other.KnownBodyCount
	q.LargeBodyCount += other.LargeBodyCount
	q.Finalize()
}
func (q *TimingQuality) Finalize() {
	if q.CandidateCount > 0 {
		v := float64(q.CompleteCount) / float64(q.CandidateCount)
		q.CoverageRatio = &v
	} else {
		q.CoverageRatio = nil
	}
}

type TimingQuantiles struct {
	N         int64                      `json:"n"`
	P10       *float64                   `json:"p10"`
	P50       *float64                   `json:"p50"`
	P90       *float64                   `json:"p90"`
	P95       *float64                   `json:"p95"`
	P10Bounds *requesttimingstats.Bounds `json:"p10_bounds"`
	P50Bounds *requesttimingstats.Bounds `json:"p50_bounds"`
	P90Bounds *requesttimingstats.Bounds `json:"p90_bounds"`
	P95Bounds *requesttimingstats.Bounds `json:"p95_bounds"`
}

type RequestTimingPoint struct {
	Finalized   bool            `json:"finalized"`
	Pending     bool            `json:"pending"`
	BucketStart time.Time       `json:"bucket_start"`
	Quality     TimingQuality   `json:"quality"`
	Upload      TimingQuantiles `json:"upload_ms"`
	Rate        TimingQuantiles `json:"upload_kbps"`
	Body        TimingQuantiles `json:"body_bytes"`
	Real        TimingQuantiles `json:"real_ttfb_ms"`
}
type RequestTimingTrend struct {
	Aggregation       *TimingAggregationHealth `json:"aggregation"`
	Collector         map[string]any           `json:"collector,omitempty"`
	Mode              string                   `json:"mode"`
	HistVersion       int                      `json:"hist_version"`
	BucketSeconds     int                      `json:"bucket_seconds"`
	StartTime         time.Time                `json:"start_time"`
	EndTime           time.Time                `json:"end_time"`
	FinalizedThrough  *time.Time               `json:"finalized_through"`
	ComputedAt        *time.Time               `json:"computed_at"`
	Provisional       bool                     `json:"provisional"`
	DegradationReason *string                  `json:"degradation_reason"`
	Quality           TimingQuality            `json:"quality"`
	Points            []RequestTimingPoint     `json:"points"`
}

type TimingAggregationHealth struct {
	Status           string     `json:"status"`
	ServerTime       time.Time  `json:"server_time"`
	TargetThrough    time.Time  `json:"target_through"`
	FinalizedThrough *time.Time `json:"finalized_through"`
	LagSeconds       *int64     `json:"lag_seconds"`
	LastRunAt        *time.Time `json:"last_run_at"`
	LastSuccessAt    *time.Time `json:"last_success_at"`
	LastErrorAt      *time.Time `json:"last_error_at"`
	LastError        *string    `json:"last_error"`
	PendingMinutes   int64      `json:"pending_minutes"`
	OldestPendingAt  *time.Time `json:"oldest_pending_at"`
}
type RequestTimingGroupPoint struct {
	BucketStart time.Time `json:"bucket_start"`
	N           int64     `json:"n"`
	P95         *float64  `json:"p95"`
}
type RequestTimingSeries struct {
	Key         string                    `json:"key"`
	Label       string                    `json:"label"`
	SampleCount int64                     `json:"sample_count"`
	Points      []RequestTimingGroupPoint `json:"points"`
}
type RequestTimingGroups struct {
	Mode          string                `json:"mode"`
	Dimension     string                `json:"dimension"`
	BucketSeconds int                   `json:"bucket_seconds"`
	StartTime     time.Time             `json:"start_time"`
	EndTime       time.Time             `json:"end_time"`
	Series        []RequestTimingSeries `json:"series"`
}
type RequestTimingSlow struct {
	Items     []*RequestTimingDetail `json:"items"`
	Sort      string                 `json:"sort"`
	StartTime time.Time              `json:"start_time"`
	EndTime   time.Time              `json:"end_time"`
}

type RequestTimingFilter struct {
	StartTime     time.Time
	EndTime       time.Time
	Platform      string
	GroupID       *int64
	AccountID     *int64
	Model         string
	BodyBytesMin  *int64
	Mode          string
	BucketSeconds int
	Dimension     string
	Top           int
	Entities      []string
	Sort          string
}

func (f *RequestTimingFilter) Normalize(now time.Time) error {
	if f.EndTime.IsZero() {
		f.EndTime = now.UTC()
	}
	if f.StartTime.IsZero() {
		f.StartTime = f.EndTime.Add(-time.Hour)
	}
	f.StartTime = f.StartTime.UTC()
	f.EndTime = f.EndTime.UTC()
	if !f.StartTime.Before(f.EndTime) || f.EndTime.Sub(f.StartTime) > 7*24*time.Hour {
		return fmt.Errorf("timing range must be positive and at most 7 days")
	}
	if f.Mode == "" {
		f.Mode = "auto"
	}
	if f.Mode != "auto" && f.Mode != "raw" && f.Mode != "preagg" {
		return fmt.Errorf("invalid timing query mode")
	}
	d := f.EndTime.Sub(f.StartTime)
	minimum := 60
	if d > 12*time.Hour {
		minimum = 300
	}
	if d > 24*time.Hour {
		minimum = 900
	}
	if f.BucketSeconds == 0 {
		f.BucketSeconds = minimum
		for _, candidate := range []int{60, 300, 900} {
			if candidate >= minimum && timingPointCount(f.StartTime, f.EndTime, candidate) <= 720 {
				f.BucketSeconds = candidate
				break
			}
		}
	}
	if (f.BucketSeconds != 60 && f.BucketSeconds != 300 && f.BucketSeconds != 900) || f.BucketSeconds < minimum || timingPointCount(f.StartTime, f.EndTime, f.BucketSeconds) > 720 {
		return fmt.Errorf("timing bucket exceeds 720-point budget")
	}
	if f.Top == 0 {
		f.Top = 5
	}
	if f.Top < 1 || f.Top > 10 || len(f.Entities) > 10 {
		return fmt.Errorf("at most 10 timing entities are allowed")
	}
	if f.Dimension != "" && f.Dimension != "account" && f.Dimension != "model" {
		return fmt.Errorf("timing dimension must be account or model")
	}
	if len(f.Entities) > 0 && f.Dimension == "" {
		f.Dimension = "account"
	}
	for _, entity := range f.Entities {
		if entity == "" || len(entity) > 100 {
			return fmt.Errorf("invalid timing entity")
		}
		if f.Dimension == "account" {
			id, err := strconv.ParseInt(entity, 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("timing account IDs must be positive integers")
			}
		}
	}
	if f.Sort == "" {
		f.Sort = "upload_ms"
	}
	if f.Sort != "upload_ms" && f.Sort != "upstream_ttft_ms" && f.Sort != "real_ttfb_ms" {
		return fmt.Errorf("invalid timing sort")
	}
	return nil
}

func timingPointCount(start, end time.Time, seconds int) int {
	if seconds <= 0 || !start.Before(end) {
		return 0
	}
	width := time.Duration(seconds) * time.Second
	first := start.Truncate(width)
	return int((end.Sub(first)-1)/width) + 1
}

// Separate optional interface avoids changing every existing OpsRepository mock.
type RequestTimingRepository interface {
	GetRequestTimingTrend(context.Context, *RequestTimingFilter) (*RequestTimingTrend, error)
	GetRequestTimingGroups(context.Context, *RequestTimingFilter) (*RequestTimingGroups, error)
	GetRequestTimingSlow(context.Context, *RequestTimingFilter) (*RequestTimingSlow, error)
	AggregateRequestTiming(context.Context, time.Time) error
}

func (s *OpsService) timingRepository(ctx context.Context) (RequestTimingRepository, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.cfg != nil && !s.cfg.RequestTiming.QueryEnabled {
		return nil, ErrOpsDisabled
	}
	r, ok := s.opsRepo.(RequestTimingRepository)
	if !ok {
		return nil, fmt.Errorf("request timing repository unavailable")
	}
	return r, nil
}
func (s *OpsService) GetRequestTimingTrend(ctx context.Context, f *RequestTimingFilter) (*RequestTimingTrend, error) {
	r, e := s.timingRepository(ctx)
	if e != nil {
		return nil, e
	}
	result, err := r.GetRequestTimingTrend(ctx, f)
	if err != nil {
		return nil, err
	}
	if s.requestTimingCollector != nil {
		copy := *result
		copy.Collector = s.requestTimingCollector()
		return &copy, nil
	}
	return result, nil
}

func (s *OpsService) SetRequestTimingCollector(provider func() map[string]any) {
	if s != nil {
		s.requestTimingCollector = provider
	}
}
func (s *OpsService) GetRequestTimingGroups(ctx context.Context, f *RequestTimingFilter) (*RequestTimingGroups, error) {
	r, e := s.timingRepository(ctx)
	if e != nil {
		return nil, e
	}
	return r.GetRequestTimingGroups(ctx, f)
}
func (s *OpsService) GetRequestTimingSlow(ctx context.Context, f *RequestTimingFilter) (*RequestTimingSlow, error) {
	r, e := s.timingRepository(ctx)
	if e != nil {
		return nil, e
	}
	return r.GetRequestTimingSlow(ctx, f)
}

func IsRequestTimingMetric(metric string) bool {
	switch metric {
	case "upload_ms_p90", "upload_kbps_p50", "large_body_ratio", "real_ttfb_ms_p95":
		return true
	}
	return false
}
