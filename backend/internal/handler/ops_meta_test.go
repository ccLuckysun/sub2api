package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type metaSampleSink chan monitoring.OpsSample

func (s metaSampleSink) Submit(sample monitoring.OpsSample) bool {
	select {
	case s <- sample:
		return true
	default:
		return false
	}
}

func TestOpsMetaRequestIDDimensionsFailoverAndCoverage(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(registry)
	require.NoError(t, err)
	runtime := monitoring.NewOpsRuntime(metrics)
	runtime.Accounts = monitoring.NewAccountLabels([]string{"first", "second"})
	var minute atomic.Int64
	base := time.Unix(1700000000, 0)
	runtime.Coverage = monitoring.NewOpsCoverage(metrics.Coverage, func() time.Time { return base.Add(time.Duration(minute.Load()) * time.Minute) })
	samples := make(metaSampleSink, 1)
	runtime.SampleSink = samples
	cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
	groupID := int64(44)
	apiKey := &service.APIKey{ID: 22, GroupID: &groupID, Group: &service.Group{ID: 44, Platform: service.PlatformComposite}}
	router := gin.New()
	router.Use(middleware2.RequestLogger())
	router.Use(middleware2.OpsMiddleware(cfg, runtime))
	router.POST("/v1/responses", func(c *gin.Context) {
		rec := monitoring.RecorderFromContext(c.Request.Context())
		require.NotNil(t, rec)
		originalModel := "gpt-CODEX-raw"
		attachOpsRequestMeta(c, apiKey, 11, originalModel, nil)
		require.Equal(t, "client-x-request", rec.RequestID())
		rec.MarkBodyRead(0, 0, "complete")
		for _, account := range []*service.Account{{ID: 33, Name: "first", Platform: service.PlatformAnthropic}, {ID: 55, Name: "second", Platform: service.PlatformOpenAI}} {
			attachOpsRequestMeta(c, apiKey, 11, originalModel, account)
			a := rec.StartAttempt(account.Name)
			a.Sent()
			a.NoteHeaders(200)
			a.UseResponse()
			a.NoteFirstByte()
			a.BodyDone(nil)
			if account.ID == 33 {
				a.Done("upstream_error", "upstream_server_error", monitoring.FailureEvidence{})
			} else {
				a.Done("success", "", monitoring.FailureEvidence{})
			}
		}
		c.Header("Content-Type", "text/event-stream")
		c.String(200, "data: unchanged\n\n")
		c.Writer.Flush()
	})
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}"))
	request.Header.Set("x-request-id", "client-x-request")
	request = request.WithContext(context.WithValue(request.Context(), ctxkey.ClientRequestID, "internal-client-uuid"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "data: unchanged\n\n", response.Body.String())
	require.True(t, response.Flushed)
	require.Equal(t, "client-x-request", response.Header().Get("X-Request-ID"))
	sample := <-samples
	require.Equal(t, "client-x-request", sample.RequestID)
	require.NotEqual(t, "internal-client-uuid", sample.RequestID)
	require.NotNil(t, sample.ClientRequestID)
	require.Equal(t, "internal-client-uuid", *sample.ClientRequestID)
	require.EqualValues(t, 11, *sample.UserID)
	require.EqualValues(t, 22, *sample.APIKeyID)
	require.EqualValues(t, 55, *sample.AccountID)
	require.EqualValues(t, 44, *sample.GroupID)
	require.Equal(t, service.PlatformOpenAI, *sample.Platform)
	require.Equal(t, "gpt-CODEX-raw", *sample.Model)
	require.Equal(t, "/v1/responses", *sample.Endpoint)
	require.Equal(t, "responses", *sample.EndpointClass)
	require.Equal(t, 2, *sample.AttemptCount)
	require.Equal(t, 1, *sample.FailedAttempts)
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.ModelRequests.WithLabelValues("codex", "success")))
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.ModelRequests.WithLabelValues("codex", "upstream_error")))
	minute.Store(1)
	runtime.Coverage.Flush()
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.Coverage.WithLabelValues("user")))
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.Coverage.WithLabelValues("account")))
}
func TestOpsMetaIDFallbackAndZeroValues(t *testing.T) {
	for _, tc := range []struct{ name, ctxID, header, want string }{
		{"ctx takes precedence", "ctx-id", "header-id", "ctx-id"},
		{"header fallback", "", "header-id", "header-id"},
		{"empty stays internal", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec, _ := opsRequestFixture(t)
			ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "not-the-request-id")
			if tc.ctxID != "" {
				ctx = context.WithValue(ctx, ctxkey.RequestID, tc.ctxID)
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			c.Request.Header.Set("x-request-id", tc.header)
			before := rec.RequestID()
			attachOpsRequestMeta(c, nil, 0, "", nil)
			if tc.want == "" {
				require.Equal(t, before, rec.RequestID())
				require.Regexp(t, `^internal-[0-9]+-[0-9]+$`, rec.RequestID())
			} else {
				require.Equal(t, tc.want, rec.RequestID())
			}
			require.Equal(t, tc.header, c.Request.Header.Get("x-request-id"), "metadata does not rewrite request headers")
			require.Empty(t, response.Body.String())
			require.Zero(t, response.Code-200)
			rec.Finalize(monitoring.FinalizeInput{Status: 401})
		})
	}
}
func TestOpsMetaNoRecorderIsNoop(t *testing.T) {
	attachOpsRequestMeta(nil, nil, 0, "", nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	attachOpsRequestMeta(c, nil, 0, "", nil)
	c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader("unchanged"))
	before := c.Request
	attachOpsRequestMeta(c, &service.APIKey{ID: 22}, 11, "raw-model", &service.Account{ID: 33})
	require.Same(t, before, c.Request)
	require.Nil(t, monitoring.RecorderFromContext(c.Request.Context()))
}
func TestOpsMetaClientRequestIDCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"present", "correlation-uuid", "correlation-uuid"},
		{"trimmed", "  correlation-uuid  ", "correlation-uuid"},
		{"missing", nil, ""},
		{"non-string ignored", 42, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics, err := monitoring.NewOpsMetrics(prometheus.NewRegistry())
			require.NoError(t, err)
			samples := make(metaSampleSink, 1)
			ctx := monitoring.WithOpsRecorderDependencies(context.Background(), nil, nil, samples)
			ctx, rec := monitoring.BeginGatewayRequest(ctx, "/v1/messages", metrics)
			if tc.value != nil {
				ctx = context.WithValue(ctx, ctxkey.ClientRequestID, tc.value)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			attachOpsRequestMeta(c, nil, 0, "", nil)
			rec.Finalize(monitoring.FinalizeInput{Status: 200})
			sample := <-samples
			if tc.want == "" {
				require.Nil(t, sample.ClientRequestID)
				return
			}
			require.NotNil(t, sample.ClientRequestID)
			require.Equal(t, tc.want, *sample.ClientRequestID)
		})
	}
}
func TestOpsMetaFallbackGroupUpdatesWholeSnapshot(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(registry)
	require.NoError(t, err)
	samples := make(metaSampleSink, 1)
	ctx := monitoring.WithOpsRecorderDependencies(context.Background(), nil, nil, samples)
	ctx, rec := monitoring.BeginGatewayRequest(ctx, "/v1/messages", metrics)
	ctx = context.WithValue(ctx, ctxkey.RequestID, "fallback-id")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	first, second := int64(10), int64(20)
	key := &service.APIKey{ID: 22, GroupID: &first}
	attachOpsRequestMeta(c, key, 11, "raw-codex", &service.Account{ID: 30, Platform: service.PlatformAnthropic})
	updated := *key
	updated.GroupID = &second
	attachOpsRequestMeta(c, &updated, 11, "raw-codex", &service.Account{ID: 40, Platform: service.PlatformOpenAI})
	rec.Finalize(monitoring.FinalizeInput{Status: 200})
	sample := <-samples
	require.Equal(t, "fallback-id", sample.RequestID)
	require.EqualValues(t, 20, *sample.GroupID)
	require.EqualValues(t, 40, *sample.AccountID)
	require.EqualValues(t, 11, *sample.UserID)
	require.EqualValues(t, 22, *sample.APIKeyID)
	require.Equal(t, "raw-codex", *sample.Model)
}

func TestOpsMetaEnrichedDisplayFieldsAndMasking(t *testing.T) {
	require.Equal(t, "sk-12345…wxyz", maskOpsAPIKey("sk-12345678901234567890wxyz"))
	require.Equal(t, "…", maskOpsAPIKey("short-key"))
	require.Equal(t, "", maskOpsAPIKey("   "))

	registry := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(registry)
	require.NoError(t, err)
	samples := make(metaSampleSink, 1)
	ctx := monitoring.WithOpsRecorderDependencies(context.Background(), nil, nil, samples)
	ctx, rec := monitoring.BeginGatewayRequest(ctx, "/v1/messages", metrics)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)

	groupID := int64(7)
	apiKey := &service.APIKey{
		ID:      34,
		Name:    "prod-client-key",
		Key:     "sk-abcdefgh1234567890123456wxyz",
		GroupID: &groupID,
		User:    &service.User{ID: 12, Email: "ops@example.com"},
		Group:   &service.Group{ID: 7, Name: "claude-pro", Platform: service.PlatformAnthropic},
	}
	account := &service.Account{ID: 5, Name: "anthropic-us-1", Platform: service.PlatformAnthropic}
	attachOpsRequestMeta(c, apiKey, 12, "claude-sonnet-4-5", account)
	rec.Finalize(monitoring.FinalizeInput{Status: 200})

	sample := <-samples
	require.NotNil(t, sample.UserEmail)
	require.Equal(t, "ops@example.com", *sample.UserEmail)
	require.NotNil(t, sample.APIKeyName)
	require.Equal(t, "prod-client-key", *sample.APIKeyName)
	require.NotNil(t, sample.APIKeyMasked)
	require.Equal(t, "sk-abcde…wxyz", *sample.APIKeyMasked)
	require.NotNil(t, sample.AccountName)
	require.Equal(t, "anthropic-us-1", *sample.AccountName)
	require.NotNil(t, sample.GroupName)
	require.Equal(t, "claude-pro", *sample.GroupName)
}

func TestOpsMetaPreservesCompositePublicModelAfterRewrite(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := monitoring.NewOpsMetrics(registry)
	require.NoError(t, err)
	samples := make(metaSampleSink, 1)
	ctx := monitoring.WithOpsRecorderDependencies(context.Background(), nil, nil, samples)
	ctx, rec := monitoring.BeginGatewayRequest(ctx, "/v1/responses", metrics)
	ctx = service.WithCompositeRouteDecision(ctx, service.CompositeRouteDecision{
		Matched:        true,
		PublicModel:    "public-alias-v1",
		TargetPlatform: service.PlatformOpenAI,
		UpstreamModel:  "gpt-4o",
	})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	groupID := int64(77)
	key := &service.APIKey{
		ID:      35,
		GroupID: &groupID,
		Group:   &service.Group{ID: 77, Name: "comp-group", Platform: service.PlatformComposite},
	}
	// Handler extracted rewritten upstream model "gpt-4o" from rewritten body;
	// attachOpsRequestMeta must still keep the public alias "public-alias-v1".
	attachOpsRequestMeta(c, key, 12, "gpt-4o", &service.Account{ID: 5, Name: "openai-acc", Platform: service.PlatformOpenAI})
	rec.Finalize(monitoring.FinalizeInput{Status: 500})

	sample := <-samples
	require.NotNil(t, sample.Model)
	require.Equal(t, "public-alias-v1", *sample.Model)
	require.NotNil(t, sample.Platform)
	require.Equal(t, service.PlatformOpenAI, *sample.Platform)
}
