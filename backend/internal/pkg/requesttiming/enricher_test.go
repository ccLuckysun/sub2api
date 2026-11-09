package requesttiming

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnricherCanceledDrainStopsWorkersBeforeDatabaseClose(t *testing.T) {
	db, mock := newEnricherMock(t)
	_ = mock
	e := NewEnricher(db, EnricherConfig{Enabled: true, Workers: 1, FlushInterval: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Drain(ctx); err != nil && err != context.Canceled {
		t.Fatal(err)
	}
	if e.Enqueue(testPatch(100, time.Now(), 123)) {
		t.Fatal("drained worker accepted work")
	}
}

func TestEnricherDeduplicationIncludesPartitionTimestamp(t *testing.T) {
	at := time.Now().UTC()
	patches := dedupePatches([]TimingPatch{testPatch(1, at, 100), testPatch(1, at, 100), testPatch(1, at.Add(time.Hour), 200)})
	if len(patches) != 2 {
		t.Fatalf("same id in two time partitions was conflated: %+v", patches)
	}
}

func newEnricherMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func testPatch(id int64, at time.Time, latency int) TimingPatch {
	return TimingPatch{
		Row:              RowRef{ID: id, CreatedAt: at, APIKeyID: 1, RequestID: "req"},
		GatewayLatencyMs: latency,
		MinuteStart:      at.UTC().Truncate(time.Minute),
		Reason:           ReasonLateRegister,
	}
}

func drainEnricher(t *testing.T, e *Enricher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestEnricher_AppliesPatchesAndMarksOnlyAppliedMinutesDirty(t *testing.T) {
	db, mock := newEnricherMock(t)
	base := time.Date(2026, 11, 8, 12, 34, 0, 0, time.UTC)
	rowA := base.Add(10 * time.Second)
	rowB := base.Add(20 * time.Second)
	rowC := base.Add(70 * time.Second)
	missing := base.Add(30 * time.Second)

	mock.MatchExpectationsInOrder(false)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(120, int64(1), rowA).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(130, int64(2), rowB).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(2)))
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(140, int64(3), rowC).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(150, int64(4), missing).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("INSERT INTO ops_request_timing_dirty").
		WithArgs(base, ReasonLateRegister).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO ops_request_timing_dirty").
		WithArgs(base.Add(time.Minute), ReasonLateRegister).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	e := NewEnricher(db, EnricherConfig{
		Enabled:       true,
		Workers:       1,
		BatchSize:     8,
		FlushInterval: time.Millisecond,
		MaxAttempts:   1,
	})
	for _, p := range []TimingPatch{
		testPatch(1, rowA, 120),
		testPatch(2, rowB, 130),
		testPatch(3, rowC, 140),
		testPatch(4, missing, 150),
	} {
		if !e.Enqueue(p) {
			t.Fatal("Enqueue rejected a patch")
		}
	}
	drainEnricher(t, e)

	stats := e.Stats()
	if stats.Enqueued != 4 || stats.Batches != 1 || stats.PatchesApplied != 3 || stats.PatchMisses != 1 || stats.DirtyMinutes != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnricher_RetriesTransientFailure(t *testing.T) {
	db, mock := newEnricherMock(t)
	at := time.Date(2026, 11, 8, 1, 2, 0, 0, time.UTC)

	mock.ExpectBegin().WillReturnError(errors.New("transient"))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(77, int64(10), at).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(10)))
	mock.ExpectExec("INSERT INTO ops_request_timing_dirty").
		WithArgs(at.Truncate(time.Minute), ReasonLateRegister).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	e := NewEnricher(db, EnricherConfig{
		Enabled:       true,
		Workers:       1,
		BatchSize:     1,
		FlushInterval: time.Millisecond,
		MaxAttempts:   2,
	})
	if !e.Enqueue(testPatch(10, at, 77)) {
		t.Fatal("Enqueue rejected first patch")
	}
	drainEnricher(t, e)

	stats := e.Stats()
	if stats.Retries != 1 || stats.PatchesApplied != 1 || stats.FailedBatches != 0 || stats.ExhaustedBatches != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnricher_UpdateMissDoesNotMarkDirty(t *testing.T) {
	db, mock := newEnricherMock(t)
	at := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE usage_logs SET gateway_latency_ms").
		WithArgs(88, int64(11), at).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	e := NewEnricher(db, EnricherConfig{
		Enabled:       true,
		Workers:       1,
		BatchSize:     1,
		FlushInterval: time.Millisecond,
		MaxAttempts:   1,
	})
	if !e.Enqueue(testPatch(11, at, 88)) {
		t.Fatal("Enqueue rejected patch")
	}
	drainEnricher(t, e)

	stats := e.Stats()
	if stats.PatchesApplied != 0 || stats.PatchMisses != 1 || stats.DirtyMinutes != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestEnricher_DeduplicatesRowsWithinBatch(t *testing.T) {
	at := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	patches := dedupePatches([]TimingPatch{
		testPatch(1, at, 10),
		testPatch(1, at, 20),
		{Row: RowRef{}, GatewayLatencyMs: 30},
		testPatch(2, at, 40),
	})
	if len(patches) != 2 || patches[0].Row.ID != 1 || patches[1].Row.ID != 2 {
		t.Fatalf("dedupePatches() = %+v", patches)
	}
}

func TestEnricher_DropsWhenDisabledFullOrDraining(t *testing.T) {
	disabled := NewEnricher(nil, EnricherConfig{Enabled: true})
	if disabled.Enqueue(TimingPatch{}) {
		t.Fatal("disabled enricher accepted patch")
	}
	if got := disabled.Stats().DroppedStopped; got != 1 {
		t.Fatalf("DroppedStopped = %d, want 1", got)
	}
	drainEnricher(t, disabled)

	db, _ := newEnricherMock(t)
	full := &Enricher{db: db, cfg: EnricherConfig{Enabled: true}, ch: make(chan TimingPatch, 1)}
	if !full.Enqueue(testPatch(1, time.Now(), 1)) {
		t.Fatal("first enqueue should fit")
	}
	if full.Enqueue(testPatch(2, time.Now(), 1)) {
		t.Fatal("second enqueue should hit full queue")
	}
	if got := full.Stats().DroppedQueueFull; got != 1 {
		t.Fatalf("DroppedQueueFull = %d, want 1", got)
	}

	stopped := NewEnricher(db, EnricherConfig{Enabled: true, Workers: 1, FlushInterval: time.Millisecond})
	drainEnricher(t, stopped)
	if stopped.Enqueue(testPatch(3, time.Now(), 1)) {
		t.Fatal("drained enricher accepted patch")
	}
	if got := stopped.Stats().DroppedStopped; got != 1 {
		t.Fatalf("DroppedStopped after drain = %d, want 1", got)
	}
}
