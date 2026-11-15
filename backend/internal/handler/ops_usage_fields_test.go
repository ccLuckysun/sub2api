package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsUsageFieldsFrozenBeforeAsyncAndFallback(t *testing.T) {
	for _, kind := range []string{"gateway", "openai"} {
		for _, mode := range []string{"enqueued", "no_pool_sync", "stopped_sync", "overflow_sync"} {
			for _, stream := range []bool{false, true} {
				name := kind + "/" + mode + map[bool]string{false: "/200", true: "/SSE"}[stream]
				t.Run(name, func(t *testing.T) {
					var pool *service.UsageRecordWorkerPool
					gate := make(chan struct{})
					var once sync.Once
					release := func() { once.Do(func() { close(gate) }) }
					if mode != "no_pool_sync" {
						pool = service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: "sync"})
						defer pool.Stop()
						defer release()
						if mode == "stopped_sync" {
							pool.Stop()
						}
						if mode == "enqueued" || mode == "overflow_sync" {
							started := make(chan struct{})
							require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { close(started); <-gate }))
							<-started
							if mode == "overflow_sync" {
								require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { <-gate }))
							}
						}
					}
					ctx, rec, _ := opsRequestFixture(t)
					rec.MarkBodyRead(0, 0, "complete")
					attempt := rec.StartAttempt("unknown")
					attempt.Sent()
					attempt.NoteHeaders(200)
					attempt.UseResponse()
					attempt.NoteFirstByte()
					attempt.BodyDone(nil)
					attempt.Done("success", "", monitoring.FailureEvidence{})
					body, latency := rec.UsageLogFields()
					require.NotNil(t, body)
					require.Zero(t, *body)
					require.NotNil(t, latency)
					input := &service.OpenAIRecordUsageInput{Result: &service.OpenAIForwardResult{Stream: stream}, BodyBytes: body, GatewayLatencyMs: latency}
					done := make(chan struct{})
					task := func(worker context.Context) {
						require.Nil(t, monitoring.RecorderFromContext(worker), "worker context must not retain Recorder")
						require.Equal(t, stream, input.Result.Stream)
						require.NotNil(t, input.BodyBytes)
						require.Zero(t, *input.BodyBytes)
						require.Equal(t, latency, input.GatewayLatencyMs)
						close(done)
					}
					if kind == "gateway" {
						(&GatewayHandler{usageRecordWorkerPool: pool}).submitUsageRecordTask(ctx, task)
					} else {
						(&OpenAIGatewayHandler{usageRecordWorkerPool: pool}).submitUsageRecordTask(ctx, task)
					}
					rec.AddBytes("in_wire", 999, "complete") // Later Recorder events cannot mutate captured scalars.
					rec.Finalize(monitoring.FinalizeInput{Status: 200})
					release()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("usage task did not execute")
					}
					require.Zero(t, *input.BodyBytes)
				})
			}
		}
	}
}
