package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
)

type timingSinkRecorder struct {
	mu      sync.Mutex
	patches []requesttiming.TimingPatch
}

func TestLoggerFailureAfterEligibilityNeverPublishesTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &timingSinkRecorder{}
	r := gin.New()
	r.Use(Logger(TimingOptions{Enabled: true, Sink: sink}))
	r.POST("/v1/error", func(c *gin.Context) {
		h := requesttiming.FromContext(c.Request.Context())
		h.MarkEligible()
		h.RegisterRow(requesttiming.InsertReceipt{Inserted: true, Row: requesttiming.RowRef{ID: 1, CreatedAt: time.Now()}})
		c.Status(http.StatusBadGateway)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/error", nil))
	if sink.count() != 0 {
		t.Fatal("non-2xx completion published a successful span")
	}
}

func (s *timingSinkRecorder) Enqueue(p requesttiming.TimingPatch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patches = append(s.patches, p)
	return true
}

func (s *timingSinkRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.patches)
}

func (s *timingSinkRecorder) first() requesttiming.TimingPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patches[0]
}

func TestLogger_FinalizesHandleAndEnrichesAfterLateRowRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &timingSinkRecorder{}
	r := gin.New()
	r.Use(Logger(TimingOptions{Enabled: true, Sink: sink}))

	var handle *requesttiming.Handle
	r.POST("/v1/messages", func(c *gin.Context) {
		handle = requesttiming.FromContext(c.Request.Context())
		if handle == nil {
			t.Error("handler did not receive a timing handle")
			return
		}
		handle.RecordBodyBytes(12)
		handle.MarkEligible()
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if handle == nil {
		t.Fatal("timing handle missing")
	}
	if !handle.Finalized() {
		t.Fatal("middleware must finalize the handle after the handler")
	}
	if got := handle.BodyBytes(); got == nil || *got != 12 {
		t.Fatalf("BodyBytes() = %v, want 12", got)
	}
	if got := sink.count(); got != 0 {
		t.Fatalf("sink received %d patches before the billing row was registered", got)
	}

	createdAt := time.Date(2026, 11, 8, 12, 0, 30, 0, time.UTC)
	handle.RegisterRow(requesttiming.InsertReceipt{
		Inserted: true,
		Row:      requesttiming.RowRef{ID: 55, CreatedAt: createdAt, APIKeyID: 9, RequestID: "late"},
	})

	if got := sink.count(); got != 1 {
		t.Fatalf("sink received %d patches, want 1", got)
	}
	patch := sink.first()
	if patch.Row.ID != 55 || patch.GatewayLatencyMs < 0 {
		t.Fatalf("unexpected patch: %+v", patch)
	}
	if want := createdAt.Truncate(time.Minute); !patch.MinuteStart.Equal(want) {
		t.Fatalf("MinuteStart = %v, want %v", patch.MinuteStart, want)
	}
}

func TestLogger_SkipsManagementAndProbePaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &timingSinkRecorder{}
	r := gin.New()
	r.Use(Logger(TimingOptions{Enabled: true, Sink: sink}))
	r.GET("/api/admin/usage", func(c *gin.Context) {
		if requesttiming.FromContext(c.Request.Context()) != nil {
			t.Error("/api path must not get a timing handle")
		}
		c.Status(http.StatusNoContent)
	})
	r.GET("/health", func(c *gin.Context) {
		if requesttiming.FromContext(c.Request.Context()) != nil {
			t.Error("/health must not get a timing handle")
		}
		c.Status(http.StatusOK)
	})

	for _, path := range []string{"/api/admin/usage", "/health"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	}
}

func TestLogger_CollectDisabledCreatesNoHandle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Logger(TimingOptions{Enabled: false, Sink: &timingSinkRecorder{}}))
	r.GET("/v1/models", func(c *gin.Context) {
		if requesttiming.FromContext(c.Request.Context()) != nil {
			t.Error("disabled collection still created a timing handle")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
}

func TestLogger_PanicAfterEligibleInvalidatesTimingPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &timingSinkRecorder{}
	r := gin.New()
	r.Use(Recovery())
	r.Use(Logger(TimingOptions{Enabled: true, Sink: sink}))

	var handle *requesttiming.Handle
	r.GET("/v1/panic", func(c *gin.Context) {
		handle = requesttiming.FromContext(c.Request.Context())
		handle.MarkEligible()
		panic("boom")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/panic", nil))

	if handle == nil {
		t.Fatal("timing handle missing")
	}
	if !handle.Finalized() {
		t.Fatal("deferred finalize did not run during panic")
	}
	handle.RegisterRow(requesttiming.InsertReceipt{
		Inserted: true,
		Row:      requesttiming.RowRef{ID: 77, CreatedAt: time.Now(), APIKeyID: 1, RequestID: "panic"},
	})
	if got := sink.count(); got != 0 {
		t.Fatalf("panicking request enqueued %d patches", got)
	}
}
