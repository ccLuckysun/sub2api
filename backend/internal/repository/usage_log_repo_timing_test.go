package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func timingTestUsageLog(requestID string) *service.UsageLog {
	return &service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      requestID,
		Model:          "gpt-5.4",
		RequestedModel: "gpt-5.4",
		CreatedAt:      time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
	}
}

func TestCreateWithReceipt_NotifiesOnlyWhenThisRequestInsertedRow(t *testing.T) {
	t.Run("inserted row is reported", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := &usageLogRepository{sql: db}
		log := timingTestUsageLog("req-timing-inserted")
		prepared := prepareUsageLogInsert(log)

		mock.ExpectQuery("INSERT INTO usage_logs").
			WithArgs(anySliceToDriverValues(prepared.args)...).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
				AddRow(int64(101), log.CreatedAt))

		var receipts []requesttiming.InsertReceipt
		inserted, err := repo.CreateWithReceipt(context.Background(), log, func(r requesttiming.InsertReceipt) {
			receipts = append(receipts, r)
		})
		require.NoError(t, err)
		require.True(t, inserted)
		require.Len(t, receipts, 1)
		require.True(t, receipts[0].Inserted)
		require.Equal(t, int64(101), receipts[0].Row.ID)
		require.Equal(t, log.RequestID, receipts[0].Row.RequestID)
		require.Equal(t, int64(2), receipts[0].Row.APIKeyID)
		require.True(t, receipts[0].Row.CreatedAt.Equal(log.CreatedAt))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("conflict existing row is not reported as this request", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := &usageLogRepository{sql: db}
		log := timingTestUsageLog("req-timing-conflict")
		prepared := prepareUsageLogInsert(log)

		mock.ExpectQuery("INSERT INTO usage_logs").
			WithArgs(anySliceToDriverValues(prepared.args)...).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}))
		mock.ExpectQuery("SELECT id, created_at FROM usage_logs").
			WithArgs(log.RequestID, log.APIKeyID).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), log.CreatedAt))

		calls := 0
		inserted, err := repo.CreateWithReceipt(context.Background(), log, func(requesttiming.InsertReceipt) {
			calls++
		})
		require.NoError(t, err)
		require.False(t, inserted)
		require.Zero(t, calls)
		require.Equal(t, int64(99), log.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestFlushBestEffortBatch_NotifiesWinningRowsOnly(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	insertedLog := timingTestUsageLog("req-best-effort-inserted")
	conflictLog := timingTestUsageLog("req-best-effort-conflict")
	insertedPrepared := prepareUsageLogInsert(insertedLog)
	conflictPrepared := prepareUsageLogInsert(conflictLog)

	args := make([]driver.Value, 0, len(insertedPrepared.args)*2)
	for i := 0; i < len(insertedPrepared.args)*2; i++ {
		args = append(args, sqlmock.AnyArg())
	}
	mock.ExpectQuery("WITH input").
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"request_id", "api_key_id", "id", "created_at"}).
			AddRow(insertedLog.RequestID, insertedLog.APIKeyID, int64(201), insertedLog.CreatedAt))

	var insertedReceipts []requesttiming.InsertReceipt
	var conflictReceipts []requesttiming.InsertReceipt
	insertedReq := usageLogBestEffortRequest{
		prepared: insertedPrepared,
		apiKeyID: insertedLog.APIKeyID,
		resultCh: make(chan error, 1),
		onInserted: func(r requesttiming.InsertReceipt) {
			insertedReceipts = append(insertedReceipts, r)
		},
	}
	conflictReq := usageLogBestEffortRequest{
		prepared: conflictPrepared,
		apiKeyID: conflictLog.APIKeyID,
		resultCh: make(chan error, 1),
		onInserted: func(r requesttiming.InsertReceipt) {
			conflictReceipts = append(conflictReceipts, r)
		},
	}

	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{insertedReq, conflictReq})

	require.Len(t, insertedReceipts, 1)
	require.Equal(t, int64(201), insertedReceipts[0].Row.ID)
	require.Equal(t, insertedLog.RequestID, insertedReceipts[0].Row.RequestID)
	require.Empty(t, conflictReceipts)
	require.NoError(t, <-insertedReq.resultCh)
	require.NoError(t, <-conflictReq.resultCh)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFlushBestEffortBatch_FallbackNotifiesOnlyReturnedRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	insertedLog := timingTestUsageLog("req-fallback-inserted")
	conflictLog := timingTestUsageLog("req-fallback-conflict")
	insertedPrepared := prepareUsageLogInsert(insertedLog)
	conflictPrepared := prepareUsageLogInsert(conflictLog)

	queryArgs := make([]driver.Value, 0, len(insertedPrepared.args)*2)
	for i := 0; i < len(insertedPrepared.args)*2; i++ {
		queryArgs = append(queryArgs, sqlmock.AnyArg())
	}
	mock.ExpectQuery("WITH input").
		WithArgs(queryArgs...).
		WillReturnError(errors.New("batch insert failed"))
	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(insertedPrepared.args)...).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(301), insertedLog.CreatedAt))
	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(conflictPrepared.args)...).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}))

	var receipts []requesttiming.InsertReceipt
	batch := []usageLogBestEffortRequest{
		{
			prepared: insertedPrepared,
			apiKeyID: insertedLog.APIKeyID,
			resultCh: make(chan error, 1),
			onInserted: func(r requesttiming.InsertReceipt) {
				receipts = append(receipts, r)
			},
		},
		{
			prepared: conflictPrepared,
			apiKeyID: conflictLog.APIKeyID,
			resultCh: make(chan error, 1),
			onInserted: func(r requesttiming.InsertReceipt) {
				receipts = append(receipts, r)
			},
		},
	}

	repo.flushBestEffortBatch(db, batch)

	require.Len(t, receipts, 1)
	require.Equal(t, int64(301), receipts[0].Row.ID)
	require.Equal(t, insertedLog.RequestID, receipts[0].Row.RequestID)
	require.NoError(t, <-batch[0].resultCh)
	require.NoError(t, <-batch[1].resultCh)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryDrain_FlushesAndRejectsNewBestEffortWrites(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}
	repo.ensureBestEffortBatcher()
	require.NotNil(t, repo.bestEffortBatchCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, repo.Drain(ctx))

	err := repo.CreateBestEffortWithReceipt(context.Background(), timingTestUsageLog("req-after-drain"), nil)
	require.Error(t, err)
	require.True(t, service.IsUsageLogCreateDropped(err))
	require.NoError(t, mock.ExpectationsWereMet())
}
