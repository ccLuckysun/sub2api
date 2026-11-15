package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestOpsSampleRepositoryRejectsInvalid(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewOpsSampleRepository(db)
	require.NoError(t, repo.InsertBatch(context.Background(), nil))
	require.ErrorContains(t, repo.InsertBatch(context.Background(), []monitoring.OpsSample{{ObservedAt: time.Now(), RequestID: " "}}), "empty request_id")
	require.ErrorContains(t, repo.InsertBatch(context.Background(), []monitoring.OpsSample{{RequestID: "x"}}), "missing observed_at")
	mock.ExpectExec("INSERT INTO ops_request_transfer_samples").WillReturnError(sql.ErrConnDone)
	require.ErrorIs(t, repo.InsertBatch(context.Background(), []monitoring.OpsSample{{RequestID: "x", ObservedAt: time.Now()}}), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestOpsSampleRepositoryPostgres(t *testing.T) {
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
	migration, err := migrations.FS.ReadFile("251_ops_v2_samples.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	text := "fixture"
	number := int64(1 << 40)
	integer := 123
	now := time.Now().UTC().Truncate(time.Microsecond)
	sample := monitoring.OpsSample{ObservedAt: now, RequestID: "full-sample", UserID: &number, APIKeyID: &number, AccountID: &number, GroupID: &number, Platform: &text, Model: &text, EndpointClass: &text, Endpoint: &text, Result: &text, HTTPStatus: &integer, FailureReason: &text, AttemptCount: &integer, FailedAttempts: &integer, Node: &text, BodyReadMs: &integer, GatewayMs: &integer, WaitMs: &integer, UpstreamMs: &integer, HeadersEndMs: &integer, ResponseMs: &integer, FinishMs: &integer, TotalMs: &integer, BytesInDeclared: &number, BytesInWire: &number, BytesInDecoded: &number, BytesUpReqTotal: &number, BytesRespUpstream: &number, BytesRespDownstream: &number, BytesInWireStatus: &text, BytesUpReqTotalStatus: &text, BytesRespUpstreamStatus: &text, BytesRespDownstreamStatus: &text, BytesInWireSource: &text, BytesUpReqTotalSource: &text, BytesRespUpstreamSource: &text, BytesRespDownstreamSource: &text, CreatedAt: &now}
	samples := make([]monitoring.OpsSample, 500)
	for i := range samples {
		samples[i] = sample
	}
	repo := NewOpsSampleRepository(db)
	require.NoError(t, repo.InsertBatch(context.Background(), samples))
	require.NoError(t, repo.InsertBatch(context.Background(), []monitoring.OpsSample{{ObservedAt: now, RequestID: "nullable"}}))
	var count, ids int
	require.NoError(t, db.QueryRow("SELECT count(*),count(DISTINCT id) FROM ops_request_transfer_samples").Scan(&count, &ids))
	require.Equal(t, 501, count)
	require.Equal(t, 501, ids)
	var all bool
	require.NoError(t, db.QueryRow(`SELECT observed_at=$1 AND created_at=$1 AND user_id=$2 AND api_key_id=$2 AND account_id=$2 AND group_id=$2 AND platform=$3 AND model=$3 AND endpoint_class=$3 AND endpoint=$3 AND result=$3 AND failure_reason=$3 AND node=$3 AND http_status=$4 AND attempt_count=$4 AND failed_attempts=$4 AND body_read_ms=$4 AND gateway_ms=$4 AND wait_ms=$4 AND upstream_ms=$4 AND headers_end_ms=$4 AND response_ms=$4 AND finish_ms=$4 AND total_ms=$4 AND bytes_in_declared=$2 AND bytes_in_wire=$2 AND bytes_in_decoded=$2 AND bytes_up_req_total=$2 AND bytes_resp_upstream=$2 AND bytes_resp_downstream=$2 AND bytes_in_wire_status=$3 AND bytes_up_req_total_status=$3 AND bytes_resp_upstream_status=$3 AND bytes_resp_downstream_status=$3 AND bytes_in_wire_source=$3 AND bytes_up_req_total_source=$3 AND bytes_resp_upstream_source=$3 AND bytes_resp_downstream_source=$3 FROM ops_request_transfer_samples WHERE request_id='full-sample' LIMIT 1`, now, number, text, integer).Scan(&all))
	require.True(t, all)
	require.NoError(t, db.QueryRow("SELECT user_id IS NULL AND bytes_in_wire IS NULL AND total_ms IS NULL AND created_at IS NOT NULL FROM ops_request_transfer_samples WHERE request_id='nullable'").Scan(&all))
	require.True(t, all)
	t.Log("PG18: batch 500 + nullable row inserted; all 40 columns (generated id included) verified")
}
