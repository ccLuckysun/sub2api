//go:build timingperf

// Reproducible API-level request-timing benchmark against an isolated Postgres.
//
// Seed the database with the SQL files in .dev/reports/timing-bench, then:
//
//	BENCH_DSN='postgres://bench:<password>@127.0.0.1:15433/bench?sslmode=disable' \
//	  go test -tags timingperf -run '^TestRequestTimingPerf$' -count=1 -v ./internal/repository
//
// The test never writes usage data; it only reads and clears the repository's
// in-process timing cache between samples.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	_ "github.com/lib/pq"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type perfSample struct {
	Endpoint string  `json:"endpoint"`
	N        int     `json:"n"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	MaxMs    float64 `json:"max_ms"`
	Points   int     `json:"points,omitempty"`
	Items    int     `json:"items,omitempty"`
	Status   string  `json:"status"`
	Code     int     `json:"code,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	Mode     string  `json:"mode,omitempty"`
}

func perfDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BENCH_DSN")
	if dsn == "" {
		t.Skip("BENCH_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func clearTimingCache() {
	timingCacheStore.Range(func(k, _ any) bool {
		timingCacheStore.Delete(k)
		return true
	})
}

func runTimingPerf(t *testing.T, name string, n int, fn func(context.Context) (int, error)) perfSample {
	t.Helper()
	var samples []float64
	var points int
	var lastErr error
	for i := 0; i < n; i++ {
		clearTimingCache()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		pts, err := fn(ctx)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		cancel()
		samples = append(samples, elapsed)
		if err == nil {
			points = pts
		} else {
			lastErr = err
		}
	}
	sort.Float64s(samples)
	s := perfSample{Endpoint: name, N: n, P50Ms: samples[len(samples)/2], P95Ms: samples[len(samples)-1], MaxMs: samples[len(samples)-1], Points: points, Status: "ok"}
	if lastErr != nil {
		s.Status = "error"
		s.Code = infraerrors.Code(lastErr)
		s.Reason = infraerrors.Reason(lastErr)
	}
	return s
}

func TestRequestTimingPerf(t *testing.T) {
	db := perfDB(t)
	repo := NewOpsRepository(db).(*opsRepository)
	end := time.Now().UTC().Truncate(time.Minute)

	var rows int64
	var size string
	if err := db.QueryRow(`SELECT count(*), pg_size_pretty(pg_total_relation_size('usage_logs')) FROM usage_logs`).Scan(&rows, &size); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("dataset rows=%d total=%s\n", rows, size)

	base := func(start time.Time, mode string, bucket int) *service.RequestTimingFilter {
		return &service.RequestTimingFilter{StartTime: start, EndTime: end, Mode: mode, BucketSeconds: bucket, Top: 5}
	}

	out := []perfSample{}

	out = append(out, runTimingPerf(t, "trend-1h-raw", 5, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-time.Hour), "raw", 60))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	// Hot cache path: the same authorized filter inside the 30s cache window.
	clearTimingCache()
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := repo.GetRequestTimingTrend(wctx, base(end.Add(-time.Hour), "raw", 60)); err != nil {
		t.Logf("warm cache: %v", err)
	}
	wcancel()
	var hot []float64
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		_, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-time.Hour), "raw", 60))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		hot = append(hot, float64(time.Since(start).Microseconds())/1000)
	}
	sort.Float64s(hot)
	out = append(out, perfSample{Endpoint: "trend-1h-raw-hot-cache", N: len(hot), P50Ms: hot[len(hot)/2], P95Ms: hot[len(hot)-1], MaxMs: hot[len(hot)-1], Status: "ok"})

	out = append(out, runTimingPerf(t, "trend-1h-raw-platform", 5, func(ctx context.Context) (int, error) {
		f := base(end.Add(-time.Hour), "raw", 60)
		f.Platform = "anthropic"
		r, err := repo.GetRequestTimingTrend(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-12h-auto", 5, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-12*time.Hour), "auto", 60))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-24h-auto", 5, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-24*time.Hour), "auto", 300))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-12h-preagg", 5, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-12*time.Hour), "preagg", 60))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-12h-preagg-platform", 5, func(ctx context.Context) (int, error) {
		f := base(end.Add(-12*time.Hour), "preagg", 60)
		f.Platform = "anthropic"
		r, err := repo.GetRequestTimingTrend(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-24h-preagg-full-day", 3, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-24*time.Hour), "preagg", 300))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-12h-preagg-group1", 5, func(ctx context.Context) (int, error) {
		f := base(end.Add(-12*time.Hour), "preagg", 60)
		f.GroupID = func() *int64 { v := int64(1); return &v }()
		r, err := repo.GetRequestTimingTrend(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-24h-preagg-group1", 3, func(ctx context.Context) (int, error) {
		f := base(end.Add(-24*time.Hour), "preagg", 300)
		f.GroupID = func() *int64 { v := int64(1); return &v }()
		r, err := repo.GetRequestTimingTrend(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-7d-auto", 3, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-7*24*time.Hour), "auto", 900))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "trend-7d-preagg", 3, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, base(end.Add(-7*24*time.Hour), "preagg", 900))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	}))

	out = append(out, runTimingPerf(t, "groups-1h-model-raw", 5, func(ctx context.Context) (int, error) {
		f := base(end.Add(-time.Hour), "raw", 60)
		f.Dimension = "model"
		r, err := repo.GetRequestTimingGroups(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Series), nil
	}))

	out = append(out, runTimingPerf(t, "groups-24h-model-raw-should-422", 3, func(ctx context.Context) (int, error) {
		f := base(end.Add(-24*time.Hour), "raw", 300)
		f.Dimension = "model"
		r, err := repo.GetRequestTimingGroups(ctx, f)
		if err != nil {
			return 0, err
		}
		return len(r.Series), nil
	}))

	slow := func(sortKey string, span time.Duration) func(context.Context) (int, error) {
		return func(ctx context.Context) (int, error) {
			f := base(end.Add(-span), "raw", 300)
			f.Sort = sortKey
			r, err := repo.GetRequestTimingSlow(ctx, f)
			if err != nil {
				return 0, err
			}
			return len(r.Items), nil
		}
	}
	out = append(out, runTimingPerf(t, "slow-1h-upload", 5, slow("upload_ms", time.Hour)))
	out = append(out, runTimingPerf(t, "slow-1h-ttft", 5, slow("upstream_ttft_ms", time.Hour)))
	out = append(out, runTimingPerf(t, "slow-1h-real", 5, slow("real_ttfb_ms", time.Hour)))
	out = append(out, runTimingPerf(t, "slow-24h-upload", 5, slow("upload_ms", 24*time.Hour)))
	out = append(out, runTimingPerf(t, "slow-24h-ttft", 5, slow("upstream_ttft_ms", 24*time.Hour)))
	out = append(out, runTimingPerf(t, "slow-24h-real", 5, slow("real_ttfb_ms", 24*time.Hour)))

	// Raw row budget: a 24h unfiltered raw read exceeds 200000 matching rows.
	// Confirm the sentinel produces 422 with the dedicated reason rather than a
	// silently truncated success.
	clearTimingCache()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	rawLogs, rawErr := repo.timingRaw(ctx, base(end.Add(-24*time.Hour), "raw", 300))
	cancel()
	budget := perfSample{Endpoint: "raw-24h-200001-sentinel", N: 1, Status: "ok"}
	if rawErr != nil {
		budget.Status = "error"
		budget.Code = infraerrors.Code(rawErr)
		budget.Reason = infraerrors.Reason(rawErr)
	} else {
		budget.Points = len(rawLogs)
	}
	out = append(out, budget)

	// Share distribution to make the report honest about skew.
	dist := struct {
		Groups    int     `json:"distinct_groups"`
		Platforms int     `json:"distinct_platforms"`
		Models    int     `json:"distinct_models"`
		Accounts  int     `json:"distinct_accounts"`
		TopShare  float64 `json:"top_model_share"`
	}{}
	_ = db.QueryRow(`SELECT count(DISTINCT group_id), count(DISTINCT lower(COALESCE(NULLIF(g.platform,''),NULLIF(a.platform,''),'unknown'))), count(DISTINCT model), count(DISTINCT account_id) FROM usage_logs ul LEFT JOIN groups g ON g.id=ul.group_id LEFT JOIN accounts a ON a.id=ul.account_id`).Scan(&dist.Groups, &dist.Platforms, &dist.Models, &dist.Accounts)
	_ = db.QueryRow(`SELECT max(c)::float8/sum(c) FROM (SELECT count(*) c FROM usage_logs GROUP BY model) s`).Scan(&dist.TopShare)

	fmt.Println("--- perf")
	for _, s := range out {
		b, _ := json.Marshal(s)
		fmt.Println(string(b))
	}
	b, _ := json.Marshal(dist)
	fmt.Println("--- distribution")
	fmt.Println(string(b))

	// Hard acceptance: each full API query must finish within 2s, and the raw
	// sentinel must map to 422 REQUEST_TIMING_BUDGET_EXCEEDED.
	for _, s := range out {
		if s.MaxMs > 2000 {
			t.Errorf("%s max %.1fms exceeds 2s", s.Endpoint, s.MaxMs)
		}
		if s.Status == "error" && s.Code != 422 {
			t.Errorf("%s returned code=%d reason=%q, want 422 budget error", s.Endpoint, s.Code, s.Reason)
		}
	}
	if budget.Code != 422 || budget.Reason != "REQUEST_TIMING_BUDGET_EXCEEDED" {
		t.Errorf("raw 200001 sentinel code=%d reason=%q, want 422 REQUEST_TIMING_BUDGET_EXCEEDED", budget.Code, budget.Reason)
	}
	if budget.Points > 0 {
		t.Errorf("raw sentinel returned %d rows, want error (no truncation)", budget.Points)
	}
}

// TestRequestTimingScopeReadPerf covers the histogram read path's missing-scope
// guard added after the rollup benchmark. It needs only the histogram table
// (seed 12h leaves, rebuild scopes, expand to 7d); it never reads usage_logs.
func TestRequestTimingScopeReadPerf(t *testing.T) {
	db := perfDB(t)
	repo := NewOpsRepository(db).(*opsRepository)

	var scopes int
	if err := db.QueryRow(`SELECT count(*) FROM ops_request_timing_minute WHERE group_id=-1`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if scopes == 0 {
		t.Skip("histogram table is not seeded")
	}

	end := time.Now().UTC().Truncate(time.Minute)
	filter := func(start time.Time, bucket int) *service.RequestTimingFilter {
		return &service.RequestTimingFilter{StartTime: start, EndTime: end, Mode: "preagg", BucketSeconds: bucket, Top: 5}
	}
	s12 := runTimingPerf(t, "scope-check-12h-preagg", 5, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, filter(end.Add(-12*time.Hour), 60))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	})
	s7 := runTimingPerf(t, "scope-check-7d-preagg", 3, func(ctx context.Context) (int, error) {
		r, err := repo.GetRequestTimingTrend(ctx, filter(end.Add(-7*24*time.Hour), 900))
		if err != nil {
			return 0, err
		}
		return len(r.Points), nil
	})
	for _, s := range []perfSample{s12, s7} {
		b, _ := json.Marshal(s)
		fmt.Println(string(b))
		if s.Status != "ok" {
			t.Errorf("%s status=%s code=%d reason=%s", s.Endpoint, s.Status, s.Code, s.Reason)
		}
		if s.MaxMs > 2000 {
			t.Errorf("%s max %.1fms exceeds 2s", s.Endpoint, s.MaxMs)
		}
	}
	if s12.P95Ms > 200 {
		t.Errorf("12h scope check p95 %.1fms exceeds 200ms target", s12.P95Ms)
	}

	// The guard itself: a leaf minute without its parent scope must produce 409
	// instead of a silent zero. Time the exact EXISTS query for both ranges.
	checkQuery := `SELECT EXISTS(SELECT 1 FROM ops_request_timing_minute l
 WHERE l.bucket_start >= $1 AND l.bucket_start < $2 AND l.group_id>=0 AND ($3='' OR l.platform=$3)
 AND NOT EXISTS(SELECT 1 FROM ops_request_timing_minute p WHERE p.bucket_start=l.bucket_start AND p.group_id=-1 AND p.platform=$3) LIMIT 1)`
	for _, span := range []struct {
		name  string
		start time.Time
	}{{"check-12h", end.Add(-12 * time.Hour)}, {"check-7d", end.Add(-7 * 24 * time.Hour)}} {
		var samples []float64
		for i := 0; i < 5; i++ {
			start := time.Now()
			var missing bool
			if err := db.QueryRow(checkQuery, span.start, end, "").Scan(&missing); err != nil {
				t.Fatal(err)
			}
			if missing {
				t.Fatalf("%s unexpectedly reports a missing global scope", span.name)
			}
			samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		}
		sort.Float64s(samples)
		fmt.Printf("{\"endpoint\":%q,\"n\":5,\"p50_ms\":%.3f,\"p95_ms\":%.3f}\n", span.name, samples[2], samples[4])
	}

	var minute time.Time
	if err := db.QueryRow(`SELECT bucket_start FROM ops_request_timing_minute WHERE bucket_start >= $1 AND bucket_start < $2 AND group_id=-1 AND platform='' ORDER BY bucket_start LIMIT 1`, end.Add(-12*time.Hour), end).Scan(&minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform=''`, minute); err != nil {
		t.Fatal(err)
	}
	clearTimingCache()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := repo.GetRequestTimingTrend(ctx, filter(end.Add(-12*time.Hour), 60))
	cancel()
	if err == nil || infraerrors.Code(err) != 409 || infraerrors.Reason(err) != "REQUEST_TIMING_AGGREGATION_PENDING" {
		t.Errorf("missing global scope: code=%d reason=%q, want 409 REQUEST_TIMING_AGGREGATION_PENDING", infraerrors.Code(err), infraerrors.Reason(err))
	}
	if err := repo.rebuildTimingScopeMinute(context.Background(), minute); err != nil {
		t.Fatalf("restore global scope: %v", err)
	}

	var platformMinute time.Time
	if err := db.QueryRow(`SELECT bucket_start FROM ops_request_timing_minute WHERE bucket_start >= $1 AND bucket_start < $2 AND group_id=-1 AND platform='anthropic' ORDER BY bucket_start LIMIT 1`, end.Add(-12*time.Hour), end).Scan(&platformMinute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform='anthropic'`, platformMinute); err != nil {
		t.Fatal(err)
	}
	clearTimingCache()
	pf := filter(end.Add(-12*time.Hour), 60)
	pf.Platform = "anthropic"
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	_, err = repo.GetRequestTimingTrend(ctx, pf)
	cancel()
	if err == nil || infraerrors.Code(err) != 409 || infraerrors.Reason(err) != "REQUEST_TIMING_AGGREGATION_PENDING" {
		t.Errorf("missing platform scope: code=%d reason=%q, want 409 REQUEST_TIMING_AGGREGATION_PENDING", infraerrors.Code(err), infraerrors.Reason(err))
	}
	if err := repo.rebuildTimingScopeMinute(context.Background(), platformMinute); err != nil {
		t.Fatalf("restore platform scope: %v", err)
	}

	clearTimingCache()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := repo.GetRequestTimingTrend(ctx, pf); err != nil {
		t.Errorf("platform query after scope restore: %v", err)
	}
	cancel()
}
