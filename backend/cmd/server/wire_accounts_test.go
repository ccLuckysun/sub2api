package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/stretchr/testify/require"
)

const accountSnapshotSQL = "SELECT name FROM accounts WHERE deleted_at IS NULL ORDER BY id"

func TestProvideAccountLabelsStartupSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	rows := sqlmock.NewRows([]string{"name"}).AddRow(" A.B ").AddRow("a b").AddRow("unknown").AddRow("!!!")
	for i := 0; i < 12; i++ {
		rows.AddRow(fmt.Sprintf("Account-%02d", i))
	}
	mock.ExpectQuery(regexp.QuoteMeta(accountSnapshotSQL)).WillReturnRows(rows).RowsWillBeClosed()
	accounts := provideAccountLabels(&monitoring.RegistryAndMetrics{Ops: &monitoring.OpsMetrics{}}, db)
	require.Equal(t, "ab", accounts.Label(" A.B "))
	for _, name := range []string{"a b", "unknown", "!!!", "ab", "new-after-startup", "Account-09", "Account-10", "Account-11"} {
		require.Equal(t, "unknown", accounts.Label(name), name)
	}
	labels := map[string]bool{"ab": true}
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("Account-%02d", i)
		require.Equal(t, fmt.Sprintf("account-%02d", i), accounts.Label(name))
		labels[accounts.Label(name)] = true
	}
	require.Len(t, labels, 10)
	// Repeated lookups reuse the same snapshot, with no second DB query.
	for i := 0; i < 100; i++ {
		require.Equal(t, "ab", accounts.Label(" A.B "))
	}
	require.NoError(t, mock.ExpectationsWereMet())
	t.Log("snapshot: 16 names read, 10 real labels + unknown; collisions/new names stay unknown")
}

func TestProvideAccountLabelsDisabledDoesNotQuery(t *testing.T) {
	for _, instruments := range []*monitoring.RegistryAndMetrics{nil, {}} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		// This expectation must remain unconsumed: any QueryContext consumes it.
		mock.ExpectQuery(regexp.QuoteMeta(accountSnapshotSQL)).WillReturnRows(sqlmock.NewRows([]string{"name"}))
		require.Nil(t, provideAccountLabels(instruments, db))
		require.ErrorContains(t, mock.ExpectationsWereMet(), "remaining expectation")
		db.Close()
	}
}

func TestProvideAccountLabelsEmptyAndFailures(t *testing.T) {
	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
	}{
		{name: "empty", rows: sqlmock.NewRows([]string{"name"})},
		{name: "query error", err: errors.New("injected DB failure")},
		{name: "scan error", rows: sqlmock.NewRows([]string{"name"}).AddRow("First").AddRow(nil)},
		{name: "iteration error", rows: sqlmock.NewRows([]string{"name"}).AddRow("First").AddRow("Second").RowError(1, errors.New("injected row failure"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			expectation := mock.ExpectQuery(regexp.QuoteMeta(accountSnapshotSQL))
			if tc.err != nil {
				expectation.WillReturnError(tc.err)
			} else {
				expectation.WillReturnRows(tc.rows).RowsWillBeClosed()
			}
			accounts := provideAccountLabels(&monitoring.RegistryAndMetrics{Ops: &monitoring.OpsMetrics{}}, db)
			require.NotNil(t, accounts)
			require.Equal(t, "unknown", accounts.Label("First"), "failures discard the entire partial snapshot")
			require.NoError(t, mock.ExpectationsWereMet())
			if tc.name != "empty" {
				require.Contains(t, logs.String(), "level=WARN")
			}
		})
	}
	logs.Reset()
	accounts := provideAccountLabels(&monitoring.RegistryAndMetrics{Ops: &monitoring.OpsMetrics{}}, nil)
	require.Equal(t, "unknown", accounts.Label("any"))
	require.Contains(t, logs.String(), "level=WARN")
}

func TestProvideAccountLabelsFiveSecondTimeout(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(accountSnapshotSQL)).WillDelayFor(6 * time.Second).WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("late"))
	start := time.Now()
	accounts := provideAccountLabels(&monitoring.RegistryAndMetrics{Ops: &monitoring.OpsMetrics{}}, db)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 4900*time.Millisecond)
	require.Less(t, elapsed, 5900*time.Millisecond)
	require.Equal(t, "unknown", accounts.Label("late"))
	require.NoError(t, mock.ExpectationsWereMet())
	t.Logf("5s context timeout: %s; empty snapshot returned", elapsed)
}
