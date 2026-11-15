package handler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func opsRequestFixture(t *testing.T) (context.Context, *monitoring.OpsRecorder, *monitoring.OpsMetrics) {
	t.Helper()
	m, err := monitoring.NewOpsMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	ctx, rec := monitoring.BeginGatewayRequest(context.Background(), "/v1/messages", m)
	return ctx, rec, m
}
func opsPhaseValue(t *testing.T, m *monitoring.OpsMetrics, phase string) (uint64, float64) {
	t.Helper()
	metric := &dto.Metric{}
	require.NoError(t, m.RequestPhase.WithLabelValues(phase).(prometheus.Metric).Write(metric))
	return metric.Histogram.GetSampleCount(), metric.Histogram.GetSampleSum()
}

func TestOpsUsageSubmitFinishModes(t *testing.T) {
	for _, gateway := range []string{"gateway", "openai"} {
		for _, mandatory := range []bool{false, true} {
			for _, mode := range []string{"enqueued", "overflow_drop", "sync_without_pool", "overflow_sync", "stopped_fallback", "nil_task"} {
				t.Run(gateway+"/"+map[bool]string{false: "regular", true: "mandatory"}[mandatory]+"/"+mode, func(t *testing.T) {
					var pool *service.UsageRecordWorkerPool
					release := make(chan struct{})
					if mode == "enqueued" || mode == "overflow_drop" || mode == "overflow_sync" || mode == "stopped_fallback" {
						policy := "drop"
						if mode == "overflow_sync" {
							policy = "sync"
						}
						pool = service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: policy})
						t.Cleanup(pool.Stop)
						if mode == "stopped_fallback" {
							pool.Stop()
						}
					}
					t.Cleanup(func() { close(release) })
					if mode == "overflow_drop" || mode == "overflow_sync" {
						started := make(chan struct{})
						require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { close(started); <-release }))
						select {
						case <-started:
						case <-time.After(time.Second):
							t.Fatal("worker did not start")
						}
						require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { <-release }))
					}
					ctx, rec, m := opsRequestFixture(t)
					rec.MarkBodyRead(0, 0, "complete")
					// Fix a zero-attempt terminal boundary before submission. This exposes
					// finish through M-1 without creating any logical upstream attempt.
					terminalBefore := time.Now()
					rec.MarkFailure("gateway_error", "unknown", monitoring.FailureEvidence{})
					terminalAfter := time.Now()
					var called atomic.Bool
					var taskReturned atomic.Pointer[time.Time]
					task := service.UsageRecordTask(func(context.Context) {
						called.Store(true)
						if mode == "enqueued" {
							<-release
						}
						now := time.Now()
						taskReturned.Store(&now)
					})
					if mode == "nil_task" {
						task = nil
					}
					if gateway == "gateway" {
						h := &GatewayHandler{usageRecordWorkerPool: pool}
						if mandatory {
							h.submitMandatoryUsageRecordTask(ctx, task)
						} else {
							h.submitUsageRecordTask(ctx, task)
						}
					} else {
						h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
						if mandatory {
							h.submitMandatoryUsageRecordTask(ctx, task)
						} else {
							h.submitUsageRecordTask(ctx, task)
						}
					}
					returnedAt := time.Now()
					rec.Finalize(monitoring.FinalizeInput{Status: 500})
					count, finish := opsPhaseValue(t, m, "finish")
					require.EqualValues(t, 1, count)
					if mode == "nil_task" {
						require.Zero(t, finish)
						require.False(t, called.Load())
					} else {
						require.Greater(t, finish, 0.0, "every nonnil submit mode marks Finish")
						require.LessOrEqual(t, finish, returnedAt.Sub(terminalBefore).Seconds(), "Finish is recorded by submit return, never at later worker completion")
						if ended := taskReturned.Load(); ended != nil {
							require.GreaterOrEqual(t, finish, ended.Sub(terminalAfter).Seconds(), "sync fallback must finish after the task returns")
						}
					}
					if mode == "enqueued" {
						require.Nil(t, taskReturned.Load(), "enqueue does not wait for worker/DB completion")
					}
					if mode == "overflow_drop" && !mandatory {
						require.False(t, called.Load())
						require.EqualValues(t, 1, pool.Stats().DroppedQueueFull)
					}
					if mode == "sync_without_pool" || mode == "stopped_fallback" || mode == "overflow_sync" || (mode == "overflow_drop" && mandatory) {
						require.True(t, called.Load())
					}
					require.Equal(t, 1.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues("gateway_error", "unknown")))
					// The task can finish later without reopening the request metrics.
					t.Logf("%s mandatory=%t mode=%s finish=%gs; no attempt created", gateway, mandatory, mode, finish)
				})
			}
		}
	}
}

func TestOpsAsyncUsageFailureCannotRewriteRequest(t *testing.T) {
	for _, gateway := range []string{"gateway", "openai"} {
		t.Run(gateway, func(t *testing.T) {
			pool := newUsageRecordTestPool(t)
			release := make(chan struct{})
			finished := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			ctx, rec, m := opsRequestFixture(t)
			task := func(context.Context) { <-release; close(finished); panic("simulated async persistence failure") }
			if gateway == "gateway" {
				(&GatewayHandler{usageRecordWorkerPool: pool}).submitUsageRecordTask(ctx, task)
			} else {
				(&OpenAIGatewayHandler{usageRecordWorkerPool: pool}).submitUsageRecordTask(ctx, task)
			}
			rec.Finalize(monitoring.FinalizeInput{Status: 200})
			require.Equal(t, 1.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues("success", "")))
			unblock()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("async task did not run")
			}
			pool.Stop() // Joins the recovered task panic before checking metrics.
			require.Equal(t, 1.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues("success", "")))
			// No worker callback receives or invokes MarkFailure on the Recorder.
			require.Equal(t, 0.0, testutil.ToFloat64(m.GatewayRequests.WithLabelValues("gateway_error", "gateway_db")))
		})
	}
}
