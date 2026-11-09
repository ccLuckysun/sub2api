//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRequestTimingDatabaseViewRawHistogramGroupsAndTop(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "timing-fixture", Platform: service.PlatformOpenAI})
	group := mustCreateGroup(t, client, &service.Group{Name: "timing-fixture", Platform: service.PlatformOpenAI})
	start := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1`, start)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, user.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, key.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, group.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	})
	insert := func(suffix string, kind int, stream, ws bool, total, body, duration, first any) int64 {
		t.Helper()
		var id int64
		err := integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,stream,request_type,openai_ws_mode,gateway_latency_ms,body_bytes,duration_ms,first_token_ms,input_tokens,cache_read_tokens,output_tokens,created_at)
 VALUES($1,$2,$3,$4,$5,'timing-model',$6,$7,$8,$9,$10,$11,$12,250000,40195,100,$13) RETURNING id`, user.ID, key.ID, account.ID, group.ID, fmt.Sprintf("client:timing-%d-%s", user.ID, suffix), stream, kind, ws, total, body, duration, first, start.Add(10*time.Second)).Scan(&id)
		require.NoError(t, err)
		return id
	}
	var fixtureID int64
	for i := 0; i < 40; i++ {
		id := insert(fmt.Sprint(i), 2, true, false, 28894, 12519746, 15494, 14069)
		if i == 0 {
			fixtureID = id
		}
	}
	insert("old", 2, true, false, nil, nil, 15494, 14069)
	insert("bad", 2, true, false, 28894, 12519746, 15494, 20000)
	for _, kind := range []int{1, 3, 4, 5} {
		insert(fmt.Sprint("unsupported", kind), kind, kind != 1, kind == 3, 28894, 12519746, 15494, 14069)
	}
	var upload, ttft, stream, real, total, ctxTokens int64
	var rate, density float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT upload_ms,upstream_ttft_ms,stream_ms,real_ttfb_ms,gateway_latency_ms,ctx_tokens,upload_kbps,bytes_per_token FROM v_request_timing WHERE id=$1`, fixtureID).Scan(&upload, &ttft, &stream, &real, &total, &ctxTokens, &rate, &density))
	require.EqualValues(t, 13400, upload)
	require.EqualValues(t, 14069, ttft)
	require.EqualValues(t, 1425, stream)
	require.EqualValues(t, 27469, real)
	require.EqualValues(t, 28894, total)
	require.EqualValues(t, 290195, ctxTokens)
	require.InDelta(t, 912.411526352612, rate, 1e-8)
	require.InDelta(t, 43.14252829993625, density, 1e-8)
	var invalidProjection int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM v_request_timing WHERE user_id=$1 AND (request_type IN(1,3,4,5) OR gateway_latency_ms IS NULL) AND upload_ms IS NOT NULL`, user.ID).Scan(&invalidProjection))
	require.Zero(t, invalidProjection)
	var badSum int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM v_request_timing WHERE user_id=$1 AND is_complete_timing AND abs(upload_ms+upstream_ttft_ms+stream_ms-gateway_latency_ms)>300`, user.ID).Scan(&badSum))
	require.Zero(t, badSum)
	r := NewOpsRepository(integrationDB).(*opsRepository)
	f := &service.RequestTimingFilter{StartTime: start, EndTime: start.Add(time.Minute), GroupID: &group.ID, Mode: "raw"}
	raw, err := r.GetRequestTimingTrend(ctx, f)
	require.NoError(t, err)
	require.Equal(t, "raw_exact", raw.Mode)
	require.Len(t, raw.Points, 1)
	require.EqualValues(t, 42, raw.Quality.CandidateCount)
	require.EqualValues(t, 40, raw.Quality.CompleteCount)
	require.EqualValues(t, 1, raw.Quality.InvalidCount)
	require.EqualValues(t, 40, raw.Points[0].Upload.N)
	require.EqualValues(t, 13400, *raw.Points[0].Upload.P50)
	minimum := int64(12519746)
	inclusive := *f
	inclusive.BodyBytesMin = &minimum
	filtered, err := r.GetRequestTimingTrend(ctx, &inclusive)
	require.NoError(t, err)
	require.EqualValues(t, 40, filtered.Points[0].Upload.N, "body_bytes_min must include its exact boundary")
	// An isolated minute recompute cannot attest that all earlier minutes are
	// finalized. Initialize its contiguous prefix explicitly in this fixture.
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET finalized_through=$1 WHERE singleton`, start)
	require.NoError(t, err)
	require.NoError(t, r.aggregateTimingMinute(ctx, start))
	f.Mode = "preagg"
	hist, err := r.GetRequestTimingTrend(ctx, f)
	require.NoError(t, err)
	require.Equal(t, "hist_approx", hist.Mode)
	require.EqualValues(t, raw.Quality.CandidateCount, hist.Quality.CandidateCount)
	bound := hist.Points[0].Upload.P95Bounds
	require.NotNil(t, bound)
	require.LessOrEqual(t, bound.Lo, 13400.0)
	require.GreaterOrEqual(t, *bound.Hi, 13400.0)
	// Scope totals are cached separately, not mixed into leaf populations.
	globalFilter := *f
	globalFilter.GroupID = nil
	global, err := r.GetRequestTimingTrend(ctx, &globalFilter)
	require.NoError(t, err)
	require.EqualValues(t, 42, global.Quality.CandidateCount)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1`, start)
	require.NoError(t, err)
	count, err := r.RebuildMissingRequestTimingScopes(ctx, 32)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 1)
	var globalN int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT candidate_count FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform=''`, start).Scan(&globalN))
	require.EqualValues(t, 42, globalN)
	// Repair the platform parent independently: the presence of a global
	// parent must not hide an incomplete per-platform scope after restart.
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform='openai'`, start)
	require.NoError(t, err)
	count, err = r.RebuildMissingRequestTimingScopes(ctx, 32)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 1)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT candidate_count FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=-1 AND platform='openai'`, start).Scan(&globalN))
	require.EqualValues(t, 42, globalN)
	f.Dimension = "account"
	groups, err := r.GetRequestTimingGroups(ctx, f)
	require.NoError(t, err)
	require.Len(t, groups.Series, 1)
	require.Equal(t, fmt.Sprint(account.ID), groups.Series[0].Key)
	require.EqualValues(t, 40, groups.Series[0].SampleCount)
	for _, sort := range []string{"upload_ms", "upstream_ttft_ms", "real_ttfb_ms"} {
		f.Sort = sort
		result, err := r.GetRequestTimingSlow(ctx, f)
		require.NoError(t, err)
		require.Len(t, result.Items, 20)
		require.EqualValues(t, 27469, *result.Items[0].Timing.RealTTFBMs)
		require.NotNil(t, result.Items[0].ClientRequestID)
	}
	// A TTL boundary changing before the minute transaction must never replace
	// its preserved distribution with the now-empty raw source.
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET source_retained_from=$1 WHERE singleton`, start.Add(time.Minute))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, user.ID)
	require.NoError(t, err)
	require.NoError(t, r.aggregateTimingMinute(ctx, start))
	var preserved int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT candidate_count FROM ops_request_timing_minute WHERE bucket_start=$1 AND group_id=$2`, start, group.ID).Scan(&preserved))
	require.EqualValues(t, 42, preserved)
	_, err = integrationDB.ExecContext(ctx, `UPDATE ops_request_timing_state SET source_retained_from=NULL WHERE singleton`)
	require.NoError(t, err)
}

func TestRequestTimingRawBudgetNeverReturnsTruncatedTopEntities(t *testing.T) {
	// Range rejection is deterministic before any source query; no accidental
	// fallback to a global histogram for per-account results is possible.
	r := NewOpsRepository(integrationDB).(*opsRepository)
	end := time.Now().UTC().Truncate(time.Minute)
	f := &service.RequestTimingFilter{StartTime: end.Add(-48 * time.Hour), EndTime: end, Dimension: "account"}
	_, err := r.GetRequestTimingGroups(context.Background(), f)
	require.ErrorContains(t, err, "REQUEST_TIMING_BUDGET_EXCEEDED")
	f.Mode = "raw"
	_, err = r.GetRequestTimingTrend(context.Background(), f)
	require.ErrorContains(t, err, "REQUEST_TIMING_BUDGET_EXCEEDED", "explicit raw must never silently switch to historical histograms")
}

func TestRequestTimingInsertReceiptsWaitForCommitAndDirtyMarkIsAtomic(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "timing-commit", Platform: service.PlatformOpenAI})
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=$1`, u.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id=$1`, key.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID)
	})
	repo := NewUsageLogRepository(client, integrationDB).(*usageLogRepository)
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint("commit=", commit), func(t *testing.T) {
			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			log := &service.UsageLog{UserID: u.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: fmt.Sprintf("client:timing-commit-%d-%t", u.ID, commit), Model: "gpt-4.1", Stream: true, RequestType: service.RequestTypeStream, CreatedAt: at}
			callbacks := 0
			inserted, err := repo.CreateWithReceipt(dbent.NewTxContext(ctx, tx), log, func(receipt requesttiming.InsertReceipt) { require.True(t, receipt.Inserted); callbacks++ })
			require.NoError(t, err)
			require.True(t, inserted)
			require.Zero(t, callbacks, "INSERT is not COMMIT")
			if commit {
				require.NoError(t, tx.Commit())
				require.Equal(t, 1, callbacks)
			} else {
				require.NoError(t, tx.Rollback())
				require.Zero(t, callbacks)
			}
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE request_id=$1`, log.RequestID).Scan(&count))
			if commit {
				require.Equal(t, 1, count)
			} else {
				require.Zero(t, count)
			}
		})
	}
	var revision int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT revision FROM ops_request_timing_dirty WHERE minute_start=$1`, at).Scan(&revision))
	require.Positive(t, revision)
	// Conflict-only insert must not pretend to create a new row or mutate dirty.
	var after int64
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,stream,request_type,created_at)
 SELECT user_id,api_key_id,account_id,request_id,model,stream,request_type,created_at FROM usage_logs WHERE user_id=$1 ON CONFLICT(request_id,api_key_id) DO NOTHING`, u.ID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT revision FROM ops_request_timing_dirty WHERE minute_start=$1`, at).Scan(&after))
	require.Equal(t, revision, after)
}

func TestRequestTimingServiceAndViewAgreeOnNullAndInvalidDiagnostics(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	f := insertTimingAlertFixture(t, tx, "openai")
	for i, sample := range []struct {
		kind                         int
		stream, ws                   bool
		total, duration, first, body any
	}{
		{2, true, false, 28894, 15494, 14069, 12519746},
		{2, true, false, nil, 100, 20, 1024},
		{2, true, false, 200, nil, 20, 1024},
		{2, true, false, 200, 100, nil, 1024},
		{2, true, false, 200, 100, 20, nil},
		{2, true, false, 100, 200, 20, 1024},
		{2, true, false, 100, 500, 20, 1024},
		{2, true, false, 200, 100, 300, 1024},
		{2, true, false, 200, 100, 20, -1},
		{1, false, false, 200, 100, -1, 1024},
		{3, true, true, 200, 100, 300, 1024},
		{4, true, false, 200, 100, 300, 1024},
		{5, true, false, nil, nil, nil, nil},
	} {
		var id int64
		at := time.Now().UTC()
		err := tx.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,request_id,stream,request_type,openai_ws_mode,gateway_latency_ms,duration_ms,first_token_ms,body_bytes,input_tokens,output_tokens,created_at)
 VALUES($1,$2,$3,$4,'gpt-4.1',$5,$6,$7,$8,$9,$10,$11,$12,100,20,$13) RETURNING id`, f.userID, f.keyID, f.accountID, f.groupID, fmt.Sprintf("timing-compare-%d", i), sample.stream, sample.kind, sample.ws, sample.total, sample.duration, sample.first, sample.body, at).Scan(&id)
		require.NoError(t, err)
		rows, err := tx.QueryContext(ctx, `SELECT `+timingLogColumns+` FROM usage_logs ul WHERE ul.id=$1`, id)
		require.NoError(t, err)
		require.True(t, rows.Next())
		log, err := scanTimingLog(rows)
		require.NoError(t, err)
		require.NoError(t, rows.Close())
		var viewJSON []byte
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT row_to_json(v) FROM v_request_timing v WHERE id=$1`, id).Scan(&viewJSON))
		serviceJSON, err := json.Marshal(service.DeriveRequestTiming(log))
		require.NoError(t, err)
		var view, derived map[string]any
		require.NoError(t, json.Unmarshal(viewJSON, &view))
		require.NoError(t, json.Unmarshal(serviceJSON, &derived))
		for _, field := range []string{"upload_ms", "upstream_ttft_ms", "stream_ms", "real_ttfb_ms", "raw_upload_ms", "timing_residual_ms", "timing_status", "reason", "is_http_stream", "is_complete_timing", "is_valid_upload", "is_valid_ttft", "is_valid_real", "is_valid_rate", "is_valid_body"} {
			require.Equal(t, view[field], derived[field], "fixture %d field %s", i, field)
		}
	}
}
