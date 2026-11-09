package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestRequestTimingCanonicalFixture(t *testing.T) {
	total, duration, first, body := 28894, 15494, 14069, int64(12519746)
	l := &UsageLog{Stream: true, RequestType: RequestTypeStream, GatewayLatencyMs: &total, DurationMs: &duration, FirstTokenMs: &first, BodyBytes: &body, InputTokens: 290195, OutputTokens: 100}
	r := DeriveRequestTiming(l)
	if !r.Complete || *r.UploadMs != 13400 || *r.StreamMs != 1425 || *r.RealTTFBMs != 27469 || *r.ResidualMs != 0 {
		t.Fatalf("bad timing: %+v", r)
	}
	if math.Abs(*r.UploadKBPS-912.411526352612) > 1e-8 || math.Abs(*r.BytesPerToken-43.14252829993625) > 1e-8 {
		t.Fatalf("bad ratios: %+v", r)
	}
	if math.Abs(*r.TPSStream-100000.0/1425) > 1e-8 {
		t.Fatal("TPS missing milliseconds conversion")
	}
	if *r.UploadMs+*r.UpstreamTTFTMs+*r.StreamMs != int64(total) {
		t.Fatal("sum mismatch")
	}
}

func TestRequestTimingNullProtocolAndAnomalyBoundaries(t *testing.T) {
	total, duration, first, zero, negative, body := 100, 80, 20, 0, -1, int64(1024)
	base := UsageLog{Stream: true, RequestType: RequestTypeStream, GatewayLatencyMs: &total, DurationMs: &duration, FirstTokenMs: &first, BodyBytes: &body}
	for _, test := range []struct {
		name   string
		mutate func(*UsageLog)
		status string
		upload bool
	}{
		{"total missing", func(l *UsageLog) { l.GatewayLatencyMs = nil }, "unavailable", false},
		{"duration missing", func(l *UsageLog) { l.DurationMs = nil }, "unavailable", false},
		{"first missing", func(l *UsageLog) { l.FirstTokenMs = nil }, "partial", true},
		{"body missing", func(l *UsageLog) { l.BodyBytes = nil }, "partial", true},
		{"sync", func(l *UsageLog) { l.Stream = false; l.RequestType = RequestTypeSync }, "unavailable", false},
		{"ws", func(l *UsageLog) { l.OpenAIWSMode = true; l.RequestType = RequestTypeWSV2 }, "unavailable", false},
		{"live", func(l *UsageLog) { l.RequestType = RequestTypeLive }, "unavailable", false},
		{"cyber", func(l *UsageLog) { l.RequestType = RequestTypeCyberBlocked }, "unavailable", false},
		{"negative", func(l *UsageLog) { l.FirstTokenMs = &negative }, "invalid", false},
		{"first beyond duration without total", func(l *UsageLog) { l.GatewayLatencyMs = nil; l.FirstTokenMs = &total }, "invalid", false},
		{"equal duration first", func(l *UsageLog) { l.FirstTokenMs = &duration }, "complete", true},
		{"zero upload", func(l *UsageLog) { l.GatewayLatencyMs = &duration }, "complete", true},
		{"all zero", func(l *UsageLog) { l.GatewayLatencyMs = &zero; l.DurationMs = &zero; l.FirstTokenMs = &zero }, "complete", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			l := base
			test.mutate(&l)
			r := DeriveRequestTiming(&l)
			if r.Status != test.status || r.ValidUpload != test.upload {
				t.Fatalf("%+v", r)
			}
			if r.UploadKBPS != nil && (math.IsInf(*r.UploadKBPS, 0) || math.IsNaN(*r.UploadKBPS)) {
				t.Fatal("non-finite rate")
			}
		})
	}
	r := DeriveRequestTiming(&UsageLog{Stream: true, RequestType: RequestTypeStream})
	if r.UploadMs != nil || r.StreamMs != nil || r.RealTTFBMs != nil {
		t.Fatal("missing values zero-filled")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if v, ok := object["gateway_latency_ms"]; !ok || v != nil {
		t.Fatal("missing values must serialize explicit null")
	}
}

func TestRequestTimingClampOverflowSafeAndFilterLimits(t *testing.T) {
	total, duration, first := 100, 200, 50
	r := DeriveRequestTiming(&UsageLog{Stream: true, RequestType: RequestTypeStream, GatewayLatencyMs: &total, DurationMs: &duration, FirstTokenMs: &first})
	if r.Status != "partial" || *r.UploadMs != 0 || r.ValidUpload {
		t.Fatal(r)
	}
	duration = 500
	r = DeriveRequestTiming(&UsageLog{Stream: true, RequestType: RequestTypeStream, GatewayLatencyMs: &total, DurationMs: &duration, FirstTokenMs: &first})
	if r.Status != "invalid" {
		t.Fatal(r)
	}
	end := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	f := RequestTimingFilter{StartTime: end.Add(-7*24*time.Hour - time.Millisecond), EndTime: end}
	if f.Normalize(end) == nil {
		t.Fatal("accepted >7d")
	}
	f = RequestTimingFilter{StartTime: end.Add(-7 * 24 * time.Hour), EndTime: end}
	if err := f.Normalize(end); err != nil || f.BucketSeconds != 900 {
		t.Fatal(f, err)
	}
	f = RequestTimingFilter{StartTime: end.Add(-24 * time.Hour), EndTime: end, BucketSeconds: 60}
	if f.Normalize(end) == nil {
		t.Fatal("accepted >720 points")
	}
}
