package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpsSampleCleanupTimeoutAndRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL statement_timeout = '15s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH batch AS").WithArgs(now.AddDate(0, 0, -30), 5000).WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	n, err := cleanupOpsSamples(context.Background(), db, now, 30)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, n)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestOpsSampleCleanupPostgres(t *testing.T) {
	dsn := os.Getenv("OPS_SAMPLE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PG18 fixture required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	var name string
	require.NoError(t, db.QueryRow("SELECT current_database()").Scan(&name))
	require.Equal(t, "w4a2b_fixture", name)
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = db.Exec("TRUNCATE TABLE ops_request_transfer_samples")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ops_request_transfer_samples(observed_at,request_id) SELECT $1::timestamptz - interval '31 days','old-'||i FROM generate_series(1,5001)i`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ops_request_transfer_samples(observed_at,request_id) VALUES ($1::timestamptz - interval '29 days','recent'),($1::timestamptz - interval '30 days','boundary'),($1,'now')`, now)
	require.NoError(t, err)
	n, err := cleanupOpsSamples(context.Background(), db, now, 30)
	require.NoError(t, err)
	require.EqualValues(t, 5001, n)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM ops_request_transfer_samples").Scan(&count))
	require.Equal(t, 3, count)
	n, err = cleanupOpsSamples(context.Background(), db, now, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM ops_request_transfer_samples").Scan(&count))
	require.Zero(t, count)
	// Exercise the actual cleanup service entry point, including config wiring.
	_, err = db.Exec(`INSERT INTO ops_request_transfer_samples(observed_at,request_id) VALUES (now()-interval '31 days','old-service'),(now(),'new-service')`)
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Monitoring.OpsSamplesEnabled = true
	cfg.Ops.SampleRetentionDays = 30
	svc := NewOpsCleanupService(nil, db, nil, cfg, nil, nil)
	svc.effective = opsCleanupEffectiveConfig{OpsCleanupConfig: config.OpsCleanupConfig{ErrorLogRetentionDays: -1, MinuteMetricsRetentionDays: -1, HourlyMetricsRetentionDays: -1}, SystemLogRetentionDays: -1}
	counts, err := svc.runCleanupOnce(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.samples)
	cfg.Ops.SampleRetentionDays = 0
	counts, err = svc.runCleanupOnce(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.samples)
	t.Log("PG18 cleanup: 5001 expired rows deleted in 5000-row batches; 30d boundary + 29d + current retained; explicit 0 truncates; real cleanup target honors config")
}

func TestOpsSampleCleanupFailureDoesNotFailOtherTargets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cfg := &config.Config{}
	cfg.Monitoring.OpsSamplesEnabled = true
	cfg.Ops.SampleRetentionDays = 30
	svc := NewOpsCleanupService(nil, db, nil, cfg, nil, nil)
	svc.effective = opsCleanupEffectiveConfig{OpsCleanupConfig: config.OpsCleanupConfig{ErrorLogRetentionDays: -1, MinuteMetricsRetentionDays: -1, HourlyMetricsRetentionDays: -1}, SystemLogRetentionDays: -1}
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL statement_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH batch AS").WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	_, err = svc.runCleanupOnce(context.Background())
	require.NoError(t, err, "sample failure is logged independently")
	require.NoError(t, mock.ExpectationsWereMet())
}
