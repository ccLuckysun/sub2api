package handler

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsOneSlotCache struct {
	service.ConcurrencyCache
	mu      sync.Mutex
	busy    bool
	blocked chan struct{}
	once    sync.Once
}

func (s *opsOneSlotCache) acquire() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		s.once.Do(func() { close(s.blocked) })
		return false, nil
	}
	s.busy = true
	return true, nil
}
func (s *opsOneSlotCache) release() error { s.mu.Lock(); s.busy = false; s.mu.Unlock(); return nil }
func (s *opsOneSlotCache) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return s.acquire()
}
func (s *opsOneSlotCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return s.acquire()
}
func (s *opsOneSlotCache) ReleaseUserSlot(context.Context, int64, string) error { return s.release() }
func (s *opsOneSlotCache) ReleaseAccountSlot(context.Context, int64, string) error {
	return s.release()
}
func (s *opsOneSlotCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (s *opsOneSlotCache) DecrementWaitCount(context.Context, int64) error { return nil }

type opsWaitSink struct{ sample monitoring.OpsSample }

func (s *opsWaitSink) Submit(sample monitoring.OpsSample) bool { s.sample = sample; return true }

func opsWaitGin(ctx context.Context) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	return c
}

func TestOpsSlotWaitConcurrencyOne(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// These P0 handlers all delegate their user/account waits to this helper.
	for _, path := range []string{"GatewayMessages", "GatewayChat", "GatewayResponses", "OpenAIShared", "Gemini", "Antigravity"} {
		for _, slot := range []string{"user", "account"} {
			t.Run(path+"/"+slot, func(t *testing.T) {
				cache := &opsOneSlotCache{blocked: make(chan struct{})}
				helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), "", time.Second)
				acquire := func(c *gin.Context) (func(), error) {
					started := false
					if slot == "user" {
						return helper.AcquireUserSlotWithWait(c, 1, 1, false, &started)
					}
					return helper.AcquireAccountSlotWithWaitTimeout(c, 1, 1, time.Second, false, &started)
				}
				ctx, rec, m := opsRequestFixture(t)
				rec.MarkBodyRead(0, 0, "complete")
				first, err := acquire(opsWaitGin(ctx))
				require.NoError(t, err)
				rec.Finalize(monitoring.FinalizeInput{Status: 200})
				_, fastWait := opsPhaseValue(t, m, "wait")
				require.Zero(t, fastWait, "immediate slot acquisition is not wait")
				ctx, _, m = opsRequestFixture(t)
				sink := &opsWaitSink{}
				ctx = monitoring.WithOpsRecorderDependencies(ctx, nil, nil, sink)
				ctx, rec = monitoring.BeginGatewayRequest(ctx, "/v1/messages", m)
				rec.MarkBodyRead(0, 0, "complete")
				type outcome struct {
					release func()
					err     error
				}
				done := make(chan outcome, 1)
				go func() { release, err := acquire(opsWaitGin(ctx)); done <- outcome{release, err} }()
				select {
				case <-cache.blocked:
				case <-time.After(time.Second):
					t.Fatal("second request never encountered occupied slot")
				}
				first()
				var second outcome
				select {
				case second = <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("second request failed to acquire after release")
				}
				require.NoError(t, second.err)
				second.release()
				a := rec.StartAttempt("unknown")
				a.Sent()
				a.Done("success", "", monitoring.FailureEvidence{})
				rec.MarkFinish()
				rec.Finalize(monitoring.FinalizeInput{Status: 200})
				_, wait := opsPhaseValue(t, m, "wait")
				require.Greater(t, wait, 0.0)
				_, gateway := opsPhaseValue(t, m, "gateway")
				require.GreaterOrEqual(t, gateway, 0.0)
				var sum float64
				for _, phase := range []string{"body_read", "gateway", "wait", "upstream", "response", "finish"} {
					count, n := opsPhaseValue(t, m, phase)
					require.EqualValues(t, 1, count)
					require.GreaterOrEqual(t, n, 0.0)
					sum += n
				}
				require.NotNil(t, sink.sample.TotalMs)
				require.InDelta(t, float64(*sink.sample.TotalMs), sum*1000, 1.01, "six exclusive phases close without headers_end")
				t.Logf("concurrency=1 fast_wait=0 second_wait=%.6fs gateway=%.6fs total=%.3fms", wait, gateway, sum*1000)
			})
		}
	}
}

func TestOpsSlotWaitTimeoutAndCancel(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		cache := &opsOneSlotCache{busy: true, blocked: make(chan struct{})}
		helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), "", time.Second)
		ctx, rec, m := opsRequestFixture(t)
		rec.MarkBodyRead(0, 0, "complete")
		ctx, cancel := context.WithCancel(ctx)
		if cancelRequest {
			go func() { <-cache.blocked; cancel() }()
		}
		started := false
		_, err := helper.AcquireAccountSlotWithWaitTimeout(opsWaitGin(ctx), 1, 1, 20*time.Millisecond, false, &started)
		require.Error(t, err)
		if cancelRequest {
			require.ErrorIs(t, err, context.Canceled)
		}
		cancel()
		rec.Finalize(monitoring.FinalizeInput{Status: 503})
		_, wait := opsPhaseValue(t, m, "wait")
		require.Greater(t, wait, 0.0)
	}
}
