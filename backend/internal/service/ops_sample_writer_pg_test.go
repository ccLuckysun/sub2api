package service_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsSampleWriterPostgres(t *testing.T) {
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
	writer := service.NewOpsSampleWriter(repository.NewOpsSampleRepository(db))
	writer.Start()
	for i := 0; i < 501; i++ {
		require.True(t, writer.Submit(monitoring.OpsSample{ObservedAt: time.Now(), RequestID: "async-pg"}))
	}
	writer.Stop()
	require.EqualValues(t, 501, writer.Stats().Written)
	require.Zero(t, writer.Stats().Dropped)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM ops_request_transfer_samples WHERE request_id='async-pg'").Scan(&count))
	require.Equal(t, 501, count)
	t.Log("PG18 real async writer: 501 accepted/written, shutdown flushed batch 500 + remainder 1")
}
