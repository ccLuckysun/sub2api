package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	stats "github.com/Wei-Shaw/sub2api/internal/pkg/requesttimingstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newTimingAlertTestRepo(t *testing.T) (*opsRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &opsRepository{db: db}, mock
}

func timingAlertExactRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"upload_ms", "upload_kbps", "real_ttfb_ms", "body_bytes",
		"is_valid_upload", "is_valid_rate", "is_valid_real", "is_valid_body",
	})
}

// The exact recheck must read the same projection as the API (v_request_timing with
// is_valid_* flags) instead of re-deriving validity inline, and must be bounded.
func TestGetRequestTimingAlertExactReadsViewProjection(t *testing.T) {
	repo, mock := newTimingAlertTestRepo(t)
	start := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	end := start.Add(time.Minute)

	mock.ExpectQuery(`FROM v_request_timing v`).
		WithArgs(start, end, service.RequestTimingRowBudget+1).
		WillReturnRows(timingAlertExactRows().
			AddRow(9000.0, nil, nil, nil, true, false, false, false).
			AddRow(200.0, nil, nil, nil, true, false, false, false).
			// Invalid rows stay in the candidate denominator but never in the quantile.
			AddRow(nil, nil, nil, nil, false, false, false, false))

	sample, err := repo.GetRequestTimingAlertExact(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: start,
		EndTime:   end,
	}, "upload_ms_p90", 0.90)
	if err != nil {
		t.Fatalf("exact recheck: %v", err)
	}
	if sample.Candidate != 3 || sample.N != 2 {
		t.Fatalf("candidate/n must be 3/2, got %+v", sample)
	}
	if sample.Coverage == nil || *sample.Coverage < 0.66 || *sample.Coverage > 0.67 {
		t.Fatalf("coverage must be 2/3, got %+v", sample.Coverage)
	}
	// percentile_cont semantics: rank=(2-1)*0.9 → 200 + 0.9*(9000-200).
	if sample.Value == nil || *sample.Value < 8119 || *sample.Value > 8121 {
		t.Fatalf("upload p90 must interpolate like percentile_cont, got %+v", sample.Value)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetRequestTimingAlertExactLargeBodyRatioUsesValidBody(t *testing.T) {
	repo, mock := newTimingAlertTestRepo(t)
	start := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	end := start.Add(time.Minute)

	mock.ExpectQuery(`FROM v_request_timing v`).
		WithArgs(start, end, service.RequestTimingRowBudget+1).
		WillReturnRows(timingAlertExactRows().
			AddRow(nil, nil, nil, float64(6*1024*1024), false, false, false, true).
			AddRow(nil, nil, nil, float64(1024), false, false, false, true).
			AddRow(nil, nil, nil, float64(9*1024*1024), false, false, false, false))

	sample, err := repo.GetRequestTimingAlertExact(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: start,
		EndTime:   end,
	}, "large_body_ratio", 0)
	if err != nil {
		t.Fatalf("exact recheck: %v", err)
	}
	if sample.N != 2 || sample.Value == nil || *sample.Value != 0.5 {
		t.Fatalf("ratio must count only valid bodies (1/2), got %+v value=%v", sample, sample.Value)
	}
}

func TestGetRequestTimingAlertExactScopesPlatformAndGroup(t *testing.T) {
	repo, mock := newTimingAlertTestRepo(t)
	start := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	group := int64(12)

	mock.ExpectQuery(`v.group_id = \$4`).
		WithArgs(start, end, "openai", group, service.RequestTimingRowBudget+1).
		WillReturnRows(timingAlertExactRows())

	sample, err := repo.GetRequestTimingAlertExact(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  "OpenAI",
		GroupID:   &group,
	}, "real_ttfb_ms_p95", 0.95)
	if err != nil {
		t.Fatalf("exact recheck: %v", err)
	}
	if sample.N != 0 || sample.Value != nil {
		t.Fatalf("empty scope must report no samples, got %+v", sample)
	}
}

func TestGetRequestTimingAlertExactStopsAtBudget(t *testing.T) {
	repo, mock := newTimingAlertTestRepo(t)
	start := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	end := start.Add(time.Minute)

	rows := timingAlertExactRows()
	for i := int64(0); i <= service.RequestTimingRowBudget; i++ {
		rows.AddRow(5000.0, nil, nil, nil, true, false, false, false)
	}
	mock.ExpectQuery(`FROM v_request_timing v`).
		WithArgs(start, end, service.RequestTimingRowBudget+1).
		WillReturnRows(rows)

	sample, err := repo.GetRequestTimingAlertExact(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: start,
		EndTime:   end,
	}, "upload_ms_p90", 0.90)
	if err != nil {
		t.Fatalf("exact recheck: %v", err)
	}
	if !sample.OverBudget || sample.Value != nil {
		t.Fatalf("over-budget scope must not answer, got %+v", sample)
	}
}

func TestGetRequestTimingAlertExactRejectsUnknownMetric(t *testing.T) {
	repo, _ := newTimingAlertTestRepo(t)
	if _, err := repo.GetRequestTimingAlertExact(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: time.Now().Add(-time.Hour),
		EndTime:   time.Now(),
	}, "upload_p95", 0.95); err == nil {
		t.Fatal("unsupported metric must error rather than fall back to a different projection")
	}
}

func TestListRequestTimingAlertStatesIsBatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	updated := time.Date(2026, 5, 1, 10, 7, 30, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"rule_id", "evaluated_bucket", "state", "sample_count", "coverage_ratio",
		"metric_value", "reason", "updated_at",
	}).
		AddRow(int64(7), updated.Add(-time.Minute), "breached", int64(120), 1.0, 9100.0, "", updated).
		AddRow(int64(9), updated.Add(-time.Minute), "unknown", int64(0), nil, nil, "histogram_boundary_unconfirmed", updated)
	mock.ExpectQuery("FROM ops_request_timing_alert_state").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	repo := &opsRepository{db: db}
	states, err := repo.ListRequestTimingAlertStates(context.Background(), []int64{7, 9, 7, 0})
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Equal(t, "breached", states[7].State)
	require.EqualValues(t, 120, states[7].SampleCount)
	require.Equal(t, "unknown", states[9].State)
	require.Equal(t, "histogram_boundary_unconfirmed", states[9].Reason)
	require.Nil(t, states[9].MetricValue, "unknown evaluations must not carry a fabricated value")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Parent summary rows are stored in the same minute table with a negative sentinel group
// id; the alert reader must only sum leaf rows or every sample would be counted twice.
func TestGetRequestTimingAlertBucketsReadsLeafRowsOnly(t *testing.T) {
	repo, mock := newTimingAlertTestRepo(t)
	start := time.Date(2026, 5, 1, 10, 5, 0, 0, time.UTC)
	bucket := start.Truncate(time.Minute)

	upload := stats.New(stats.Upload)
	for i := 0; i < 40; i++ {
		require.NoError(t, upload.Add(9000))
	}
	encode := func(h *stats.Histogram) []byte {
		data, err := h.Encode()
		require.NoError(t, err)
		return data
	}

	mock.ExpectQuery(`FROM ops_request_timing_minute[\s\S]*group_id >= 0`).
		WithArgs(bucket, bucket.Add(time.Minute)).
		WillReturnRows(sqlmock.NewRows([]string{
			"bucket_start", "candidate_count", "upload_count", "rate_count", "real_count",
			"known_body_count", "large_body_count", "hist_version", "hist_schema",
			"upload_hist", "rate_hist", "body_hist", "real_hist",
		}).AddRow(
			bucket, 40, 40, 0, 0, 0, 0, int(stats.Version), stats.SchemaChecksum(),
			encode(upload), encode(stats.New(stats.Rate)), encode(stats.New(stats.Body)), encode(stats.New(stats.Real)),
		))

	buckets, err := repo.GetRequestTimingAlertBuckets(context.Background(), &service.RequestTimingAlertFilter{
		StartTime: bucket,
		EndTime:   bucket.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	require.EqualValues(t, 40, buckets[0].UploadCount)
	require.EqualValues(t, 40, buckets[0].Upload.N)
	require.NoError(t, mock.ExpectationsWereMet())
}
