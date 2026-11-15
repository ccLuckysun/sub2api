package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
)

// OpsSampleRepository persists complete input snapshots, never a schema
// intersection. Missing/incompatible deployment columns cause a visible error.
type OpsSampleRepository struct{ db *sql.DB }

func NewOpsSampleRepository(db *sql.DB) *OpsSampleRepository { return &OpsSampleRepository{db: db} }

const opsSampleColumns = "observed_at,request_id,client_request_id,user_id,api_key_id,account_id,group_id,platform,model,endpoint_class,endpoint,result,http_status,failure_reason,attempt_count,failed_attempts,node,body_read_ms,gateway_ms,wait_ms,upstream_ms,headers_end_ms,response_ms,finish_ms,total_ms,bytes_in_declared,bytes_in_wire,bytes_in_decoded,bytes_up_req_total,bytes_resp_upstream,bytes_resp_downstream,bytes_in_wire_status,bytes_up_req_total_status,bytes_resp_upstream_status,bytes_resp_downstream_status,bytes_in_wire_source,bytes_up_req_total_source,bytes_resp_upstream_source,bytes_resp_downstream_source,created_at"

func (r *OpsSampleRepository) InsertBatch(ctx context.Context, samples []monitoring.OpsSample) error {
	if len(samples) == 0 {
		return nil
	}
	if r == nil || r.db == nil {
		return fmt.Errorf("ops sample: nil database")
	}
	var query strings.Builder
	query.WriteString("INSERT INTO ops_request_transfer_samples (" + opsSampleColumns + ") VALUES ")
	args := make([]any, 0, len(samples)*40)
	for i, s := range samples {
		if strings.TrimSpace(s.RequestID) == "" {
			return fmt.Errorf("ops sample: empty request_id at row %d", i)
		}
		if s.ObservedAt.IsZero() {
			return fmt.Errorf("ops sample: missing observed_at at row %d", i)
		}
		values := []any{s.ObservedAt, s.RequestID, s.ClientRequestID, s.UserID, s.APIKeyID, s.AccountID, s.GroupID, s.Platform, s.Model, s.EndpointClass, s.Endpoint, s.Result, s.HTTPStatus, s.FailureReason, s.AttemptCount, s.FailedAttempts, s.Node, s.BodyReadMs, s.GatewayMs, s.WaitMs, s.UpstreamMs, s.HeadersEndMs, s.ResponseMs, s.FinishMs, s.TotalMs, s.BytesInDeclared, s.BytesInWire, s.BytesInDecoded, s.BytesUpReqTotal, s.BytesRespUpstream, s.BytesRespDownstream, s.BytesInWireStatus, s.BytesUpReqTotalStatus, s.BytesRespUpstreamStatus, s.BytesRespDownstreamStatus, s.BytesInWireSource, s.BytesUpReqTotalSource, s.BytesRespUpstreamSource, s.BytesRespDownstreamSource, s.CreatedAt}
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for j, value := range values {
			if j > 0 {
				query.WriteByte(',')
			}
			placeholder := fmt.Sprintf("$%d", len(args)+1)
			if j == len(values)-1 {
				placeholder = "COALESCE(" + placeholder + "::timestamptz,now())"
			}
			query.WriteString(placeholder)
			args = append(args, value)
		}
		query.WriteByte(')')
	}
	_, err := r.db.ExecContext(ctx, query.String(), args...)
	return err
}
