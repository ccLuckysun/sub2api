package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestTimingAggregationLeaseContentionDoesNotHideEarlierFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := NewOpsRepository(db).(*opsRepository)
	now := time.Now().UTC().Truncate(time.Minute)
	bucket := now.Add(-10 * time.Minute)
	failure := errors.New("source query failed")
	mock.ExpectExec("UPDATE ops_request_timing_state SET last_run_at").WillReturnResult(sqlmock.NewResult(0, 1))
	// First choose the next source minute without advancing past it.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT finalized_through,source_retained_from").WillReturnRows(sqlmock.NewRows([]string{"through", "retained"}).AddRow(bucket, nil))
	mock.ExpectQuery("SELECT ul.created_at").WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(bucket))
	mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"deferred"}).AddRow(false))
	mock.ExpectCommit()
	// The source read fails and schedules a retry.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT source_retained_from").WillReturnRows(sqlmock.NewRows([]string{"retained"}).AddRow(nil))
	mock.ExpectQuery("SELECT revision").WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(1))
	mock.ExpectQuery("SELECT ul.id").WillReturnError(failure)
	mock.ExpectRollback()
	mock.ExpectExec("INSERT INTO ops_request_timing_dirty").WillReturnResult(sqlmock.NewResult(0, 1))
	// A peer wins the lease on the following dirty bucket. This is harmless
	// contention, but it cannot convert the earlier failure into success.
	mock.ExpectQuery("SELECT minute_start FROM ops_request_timing_dirty").WillReturnRows(sqlmock.NewRows([]string{"minute_start"}).AddRow(bucket.Add(time.Minute)))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectRollback()
	mock.ExpectExec("UPDATE ops_request_timing_state SET last_error=").WithArgs(failure.Error()).WillReturnResult(sqlmock.NewResult(0, 1))
	require.ErrorIs(t, r.AggregateRequestTiming(context.Background(), now), failure)
	require.NoError(t, mock.ExpectationsWereMet())
}
