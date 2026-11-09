//go:build timingbench

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type walResult struct {
	Phase      string  `json:"phase"`
	Rows       int     `json:"rows"`
	WallMs     float64 `json:"wall_ms"`
	WALBytes   int64   `json:"wal_bytes"`
	WALPerRow  float64 `json:"wal_bytes_per_row"`
	LogicalRow int64   `json:"logical_bytes_per_row"`
	Amplify    float64 `json:"wal_to_logical_ratio"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	Note       string  `json:"note"`
}

func benchDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BENCH_DSN")
	if dsn == "" {
		t.Skip("BENCH_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func walLSN(t *testing.T, db *sql.DB) string {
	t.Helper()
	var lsn string
	if err := db.QueryRow(`SELECT pg_current_wal_lsn()::text`).Scan(&lsn); err != nil {
		t.Fatal(err)
	}
	return lsn
}

func walDiff(t *testing.T, db *sql.DB, before, after string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT pg_wal_lsn_diff($1::pg_lsn,$2::pg_lsn)::bigint`, after, before).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// walBenchRunSalt keeps request IDs unique across repeated runs in the same
// minute, so ON CONFLICT never turns the measured INSERT into a no-op.
var walBenchRunSalt = time.Now().UnixNano()

func walBenchLog(i int, at time.Time) *service.UsageLog {
	return &service.UsageLog{
		UserID: 1 + int64(i%1000), APIKeyID: 1 + int64(i%2000), AccountID: 1 + int64(i%60),
		RequestID: fmt.Sprintf("timingbench-wal-%d-%d-%d", walBenchRunSalt, at.UnixNano(), i), Model: "bench-model-1",
		Stream: true, RequestType: service.RequestTypeStream, CreatedAt: at,
	}
}

func TestTimingBenchWAL(t *testing.T) {
	db := benchDB(t)
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `CHECKPOINT`)
	repo := &usageLogRepository{sql: db, db: db}

	const batchSize = 128
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	reqs := make([]usageLogBestEffortRequest, 0, batchSize)
	reqIDs := make([]string, 0, batchSize)
	ids := make([]int64, 0, batchSize)
	// Warm the same index pages so the measured batch is steady-state WAL rather
	// than first-touch full-page images after CHECKPOINT.
	warm := make([]usageLogBestEffortRequest, 0, batchSize)
	for i := 0; i < batchSize; i++ {
		log := walBenchLog(i, at.Add(time.Duration(i)*time.Millisecond))
		p := prepareUsageLogInsert(log)
		reqIDs = append(reqIDs, log.RequestID)
		reqs = append(reqs, usageLogBestEffortRequest{prepared: p, apiKeyID: log.APIKeyID, resultCh: make(chan error, 1)})
		wlog := walBenchLog(i, at.Add(time.Duration(i)*time.Millisecond))
		wlog.RequestID = "timingbench-warm-" + wlog.RequestID
		warm = append(warm, usageLogBestEffortRequest{prepared: prepareUsageLogInsert(wlog), apiKeyID: wlog.APIKeyID, resultCh: make(chan error, 1)})
	}
	repo.flushBestEffortBatch(db, warm)
	l0 := walLSN(t, db)
	t0 := time.Now()
	repo.flushBestEffortBatch(db, reqs)
	wallInsert := float64(time.Since(t0).Microseconds()) / 1000
	l1 := walLSN(t, db)
	var inserted int
	if err := db.QueryRow(`SELECT count(*) FROM usage_logs WHERE request_id = ANY($1)`, pq.Array(reqIDs)).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id FROM usage_logs WHERE request_id = ANY($1) ORDER BY id`, pq.Array(reqIDs))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	var logical int64
	_ = db.QueryRow(`SELECT COALESCE(sum(pg_column_size(t.*)),0) FROM usage_logs t WHERE request_id = ANY($1)`, pq.Array(reqIDs)).Scan(&logical)
	insertRes := walResult{Phase: "best_effort_insert_128", Rows: inserted, WallMs: wallInsert,
		WALBytes: walDiff(t, db, l0, l1), LogicalRow: logical / int64(maxInt(inserted, 1)),
		Note: "actual buildUsageLogBestEffortInsertQuery path, one 128-row CTE"}
	insertRes.WALPerRow = float64(insertRes.WALBytes) / float64(maxInt(inserted, 1))
	insertRes.Amplify = float64(insertRes.WALBytes) / float64(maxInt64(logical, 1))

	// Enrichment: 128 UPDATEs + one dirty UPSERT in one transaction.
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE usage_logs SET gateway_latency_ms=NULL WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	enricher := requesttiming.NewEnricher(db, requesttiming.EnricherConfig{
		Enabled: true, Workers: 1, BatchSize: 128, FlushInterval: 100 * time.Millisecond, MaxAttempts: 1,
	})
	patches := make([]requesttiming.TimingPatch, 0, len(ids))
	for i, id := range ids {
		patches = append(patches, requesttiming.TimingPatch{
			Row:              requesttiming.RowRef{ID: id, CreatedAt: at.Add(time.Duration(i) * time.Millisecond)},
			GatewayLatencyMs: 1000 + i,
			MinuteStart:      at.Truncate(time.Minute),
			Reason:           requesttiming.ReasonLateRegister,
		})
	}
	_, _ = db.ExecContext(ctx, `DELETE FROM ops_request_timing_dirty WHERE minute_start=$1`, at.Truncate(time.Minute))
	// The NULL-reset UPDATEs above touched every heap page after the last
	// checkpoint, so this measurement is steady-state rather than first-touch FPI.
	l2 := walLSN(t, db)
	t1 := time.Now()
	for _, p := range patches {
		if !enricher.Enqueue(p) {
			t.Fatal("enqueue failed")
		}
	}
	enrichCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = enricher.Drain(enrichCtx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	wallEnrich := float64(time.Since(t1).Microseconds()) / 1000
	l3 := walLSN(t, db)
	var applied int
	_ = db.QueryRow(`SELECT count(*) FROM usage_logs WHERE id = ANY($1) AND gateway_latency_ms IS NOT NULL`, pq.Array(ids)).Scan(&applied)
	enrichRes := walResult{Phase: "enrich_128_updates_1_dirty_upsert", Rows: applied, WallMs: wallEnrich,
		WALBytes: walDiff(t, db, l2, l3), Note: "actual Enricher applyBatch transaction"}
	enrichRes.WALPerRow = float64(enrichRes.WALBytes) / float64(maxInt(applied, 1))

	// Hot dirty contention: 8 concurrent enrichment transactions, each updating
	// 128 distinct rows in the SAME minute and upserting the same dirty key.
	hotAt := at.Truncate(time.Minute)
	var hotRows []int64
	var hotReqIDs []string
	for w := 0; w < 8; w++ {
		batch := make([]usageLogBestEffortRequest, 0, 128)
		for i := 0; i < 128; i++ {
			log := walBenchLog(w*128+i, hotAt)
			log.RequestID = fmt.Sprintf("timingbench-hot-%d-%d-%d", walBenchRunSalt, w, i)
			hotReqIDs = append(hotReqIDs, log.RequestID)
			p := prepareUsageLogInsert(log)
			batch = append(batch, usageLogBestEffortRequest{prepared: p, apiKeyID: log.APIKeyID, resultCh: make(chan error, 1)})
		}
		repo.flushBestEffortBatch(db, batch)
	}
	hr, err := db.Query(`SELECT id FROM usage_logs WHERE request_id = ANY($1) ORDER BY id`, pq.Array(hotReqIDs))
	if err != nil {
		t.Fatal(err)
	}
	for hr.Next() {
		var id int64
		_ = hr.Scan(&id)
		hotRows = append(hotRows, id)
	}
	hr.Close()
	if len(hotRows) < 8*128 {
		t.Fatalf("hot rows = %d, want %d", len(hotRows), 8*128)
	}
	hotRows = hotRows[:8*128]
	var wg sync.WaitGroup
	var mu sync.Mutex
	var lat []float64
	for w := 0; w < 8 && len(hotRows) >= (w+1)*128; w++ {
		chunk := hotRows[w*128 : (w+1)*128]
		wg.Add(1)
		go func(ids []int64) {
			defer wg.Done()
			e := requesttiming.NewEnricher(db, requesttiming.EnricherConfig{
				Enabled: true, Workers: 1, BatchSize: 128, FlushInterval: 100 * time.Millisecond, MaxAttempts: 1,
			})
			start := time.Now()
			for i, id := range ids {
				_ = e.Enqueue(requesttiming.TimingPatch{
					Row:              requesttiming.RowRef{ID: id, CreatedAt: at.Truncate(time.Minute).Add(time.Duration(i) * time.Millisecond)},
					GatewayLatencyMs: 2000 + i,
					MinuteStart:      at.Truncate(time.Minute),
					Reason:           "hot",
				})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = e.Drain(ctx)
			cancel()
			mu.Lock()
			lat = append(lat, float64(time.Since(start).Microseconds())/1000)
			mu.Unlock()
		}(chunk)
	}
	wg.Wait()
	sort.Float64s(lat)
	hotRes := walResult{Phase: "hot_dirty_8x128_same_minute", Rows: len(hotRows), Note: "8 concurrent Enricher instances, same dirty minute"}
	if len(lat) > 0 {
		hotRes.P50Ms = lat[len(lat)/2]
		hotRes.P95Ms = lat[len(lat)-1]
		hotRes.WallMs = lat[len(lat)-1]
	}
	var revisions int
	_ = db.QueryRow(`SELECT count(*) FROM ops_request_timing_dirty WHERE minute_start=$1`, at.Truncate(time.Minute)).Scan(&revisions)
	hotRes.Note += fmt.Sprintf("; dirty rows=%d", revisions)

	out := []walResult{insertRes, enrichRes, hotRes}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
