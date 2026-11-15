package monitoring

import "time"

// OpsSample is a value snapshot for asynchronous persistence. Nil optional
// fields represent unobserved values, including genuinely unknown byte counts.
// ClientRequestID is the correlation UUID; usage_logs.request_id prefixes it
// with "client:" (or "local:" + RequestID as the fallback).
// ID is database-generated; CreatedAt nil uses the database clock.
// Callers must not mutate pointer values after submission.
type OpsSample struct {
	ObservedAt                                                                                   time.Time
	RequestID                                                                                    string
	ClientRequestID                                                                              *string
	UserID, APIKeyID, AccountID, GroupID                                                         *int64
	Platform, Model, EndpointClass, Endpoint                                                     *string
	Result                                                                                       *string
	HTTPStatus                                                                                   *int
	FailureReason                                                                                *string
	AttemptCount, FailedAttempts                                                                 *int
	Node                                                                                         *string
	BodyReadMs, GatewayMs, WaitMs, UpstreamMs                                                    *int
	HeadersEndMs, ResponseMs, FinishMs, TotalMs                                                  *int
	BytesInDeclared, BytesInWire, BytesInDecoded                                                 *int64
	BytesUpReqTotal, BytesRespUpstream, BytesRespDownstream                                      *int64
	BytesInWireStatus, BytesUpReqTotalStatus, BytesRespUpstreamStatus, BytesRespDownstreamStatus *string
	BytesInWireSource, BytesUpReqTotalSource, BytesRespUpstreamSource, BytesRespDownstreamSource *string
	CreatedAt                                                                                    *time.Time
}

// OpsSampleSink accepts an immutable sample without waiting for queue space,
// persistence or logging. Submit returns false on rejection. Implementations
// MUST be nonblocking; the application supplies OpsSampleWriter, whose Submit
// uses a bounded channel select. Recorder never launches per-request goroutines.
type OpsSampleSink interface {
	Submit(OpsSample) bool
}
