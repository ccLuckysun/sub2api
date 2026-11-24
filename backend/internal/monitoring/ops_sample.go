package monitoring

import "time"

// OpsSample is a value snapshot for asynchronous realtime request buffering.
// Nil optional fields represent unobserved values, including genuinely unknown
// byte counts. ClientRequestID is the correlation UUID; usage_logs.request_id
// prefixes it with "client:" (or "local:" + RequestID as the fallback).
// Callers must not mutate pointer values after submission.
type OpsSample struct {
	ObservedAt                  time.Time  `json:"observed_at"`
	RequestID                   string     `json:"request_id"`
	ClientRequestID             *string    `json:"client_request_id,omitempty"`
	UserID                      *int64     `json:"user_id,omitempty"`
	UserEmail                   *string    `json:"user_email,omitempty"`
	APIKeyID                    *int64     `json:"api_key_id,omitempty"`
	APIKeyName                  *string    `json:"api_key_name,omitempty"`
	APIKeyMasked                *string    `json:"api_key_masked,omitempty"`
	AccountID                   *int64     `json:"account_id,omitempty"`
	AccountName                 *string    `json:"account_name,omitempty"`
	GroupID                     *int64     `json:"group_id,omitempty"`
	GroupName                   *string    `json:"group_name,omitempty"`
	Platform                    *string    `json:"platform,omitempty"`
	Model                       *string    `json:"model,omitempty"`
	EndpointClass               *string    `json:"endpoint_class,omitempty"`
	Endpoint                    *string    `json:"endpoint,omitempty"`
	HTTPMethod                  *string    `json:"http_method,omitempty"`
	Result                      *string    `json:"result,omitempty"`
	HTTPStatus                  *int       `json:"http_status,omitempty"`
	FailureReason               *string    `json:"failure_reason,omitempty"`
	ErrorMessage                *string    `json:"error_message,omitempty"`
	UpstreamStatus              *int       `json:"upstream_status,omitempty"`
	UpstreamErrorCode           *string    `json:"upstream_error_code,omitempty"`
	AttemptCount                *int       `json:"attempt_count,omitempty"`
	FailedAttempts              *int       `json:"failed_attempts,omitempty"`
	Node                        *string    `json:"node,omitempty"`
	BodyReadMs                  *int       `json:"body_read_ms,omitempty"`
	WaitMs                      *int       `json:"wait_ms,omitempty"`
	GatewayMs                   *int       `json:"gateway_ms,omitempty"`
	UpstreamMs                  *int       `json:"upstream_ms,omitempty"`
	HeadersEndMs                *int       `json:"headers_end_ms,omitempty"`
	ResponseMs                  *int       `json:"response_ms,omitempty"`
	FinishMs                    *int       `json:"finish_ms,omitempty"`
	TotalMs                     *int       `json:"total_ms,omitempty"`
	BytesInDeclared             *int64     `json:"bytes_in_declared,omitempty"`
	BytesInWire                 *int64     `json:"bytes_in_wire,omitempty"`
	BytesInDecoded              *int64     `json:"bytes_in_decoded,omitempty"`
	BytesUpReqTotal             *int64     `json:"bytes_up_req_total,omitempty"`
	BytesRespUpstream           *int64     `json:"bytes_resp_upstream,omitempty"`
	BytesRespDownstream         *int64     `json:"bytes_resp_downstream,omitempty"`
	BytesInWireStatus           *string    `json:"bytes_in_wire_status,omitempty"`
	BytesUpReqTotalStatus       *string    `json:"bytes_up_req_total_status,omitempty"`
	BytesRespUpstreamStatus     *string    `json:"bytes_resp_upstream_status,omitempty"`
	BytesRespDownstreamStatus   *string    `json:"bytes_resp_downstream_status,omitempty"`
	BytesInWireSource           *string    `json:"bytes_in_wire_source,omitempty"`
	BytesUpReqTotalSource       *string    `json:"bytes_up_req_total_source,omitempty"`
	BytesRespUpstreamSource     *string    `json:"bytes_resp_upstream_source,omitempty"`
	BytesRespDownstreamSource   *string    `json:"bytes_resp_downstream_source,omitempty"`
	CreatedAt                   *time.Time `json:"created_at,omitempty"`
}

// OpsSampleSink accepts an immutable sample without waiting for queue space,
// persistence or logging. Submit returns false on rejection. Implementations
// MUST be nonblocking; the application supplies OpsSampleWriter, whose Submit
// uses a bounded channel select. Recorder never launches per-request goroutines.
type OpsSampleSink interface {
	Submit(OpsSample) bool
}
