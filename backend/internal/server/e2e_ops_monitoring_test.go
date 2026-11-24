package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Opaque-box E2E Harness & Helpers (F1–F9)
// ---------------------------------------------------------------------------

type e2eUpstreamResponse struct {
	Status      int
	ContentType string
	Headers     map[string]string
	Body        string
	Delay       time.Duration
}

type e2eMockUpstream struct {
	server *httptest.Server

	mu         sync.Mutex
	calls      int
	httpSeq    []e2eUpstreamResponse
	httpHandler func(call int, r *http.Request, body []byte) e2eUpstreamResponse
	wsHandler   func(w http.ResponseWriter, r *http.Request)
}

func newE2EMockUpstream(t *testing.T) *e2eMockUpstream {
	t.Helper()
	m := &e2eMockUpstream{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
			m.mu.Lock()
			wsFn := m.wsHandler
			m.mu.Unlock()
			if wsFn != nil {
				wsFn(w, r)
				return
			}
			conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
				CompressionMode: coderws.CompressionContextTakeover,
			})
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			for {
				readCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				msgType, payload, readErr := conn.Read(readCtx)
				cancel()
				if readErr != nil {
					return
				}
				if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
					continue
				}
				resp := `{"type":"response.completed","response":{"id":"resp_ws_e2e","object":"response","model":"gpt-4o","status":"completed","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`
				if strings.Contains(string(payload), "gpt-5.1") {
					resp = `{"type":"response.completed","response":{"id":"resp_ws_e2e","object":"response","model":"gpt-5.1","status":"completed","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`
				}
				writeCtx, wcancel := context.WithTimeout(r.Context(), 3*time.Second)
				writeErr := conn.Write(writeCtx, coderws.MessageText, []byte(resp))
				wcancel()
				if writeErr != nil {
					return
				}
			}
		}

		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.calls++
		call := m.calls
		fn := m.httpHandler
		var resp e2eUpstreamResponse
		hasSeq := len(m.httpSeq) > 0
		if hasSeq {
			idx := call - 1
			if idx >= len(m.httpSeq) {
				idx = len(m.httpSeq) - 1
			}
			resp = m.httpSeq[idx]
		}
		m.mu.Unlock()

		if fn != nil {
			resp = fn(call, r, body)
		} else if !hasSeq {
			if strings.Contains(r.URL.Path, "messages") {
				resp = e2eUpstreamResponse{
					Status:      http.StatusOK,
					ContentType: "application/json",
					Body:        `{"id":"msg_e2e_ok","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":4}}`,
				}
			} else {
				resp = e2eUpstreamResponse{
					Status:      http.StatusOK,
					ContentType: "application/json",
					Body:        `{"id":"resp_e2e_ok","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}}`,
				}
			}
		}

		if resp.Delay > 0 {
			select {
			case <-time.After(resp.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if resp.Status == 0 {
			resp.Status = http.StatusOK
		}
		ct := resp.ContentType
		if ct == "" {
			ct = "application/json"
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.Status)
		_, _ = io.WriteString(w, resp.Body)
		if f, ok := w.(http.Flusher); ok && strings.Contains(ct, "event-stream") {
			f.Flush()
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *e2eMockUpstream) setSequence(seq ...e2eUpstreamResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = 0
	m.httpSeq = append([]e2eUpstreamResponse(nil), seq...)
	m.httpHandler = nil
}

func (m *e2eMockUpstream) setHTTPHandler(fn func(call int, r *http.Request, body []byte) e2eUpstreamResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = 0
	m.httpSeq = nil
	m.httpHandler = fn
}

func (m *e2eMockUpstream) setWSHandler(fn func(w http.ResponseWriter, r *http.Request)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsHandler = fn
}

func (m *e2eMockUpstream) URL() string {
	return m.server.URL
}

func (m *e2eMockUpstream) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// ---------------------------------------------------------------------------
// Minimal In-Memory Repository & Cache Stubs
// ---------------------------------------------------------------------------

type e2eAPIKeyRepo struct {
	service.APIKeyRepository
	mu              sync.RWMutex
	keys            map[string]*service.APIKey
	byID            map[int64]*service.APIKey
	onGetByKeyAuth  func(ctx context.Context, key string) (*service.APIKey, error)
}

func newE2EAPIKeyRepo() *e2eAPIKeyRepo {
	return &e2eAPIKeyRepo{
		keys: make(map[string]*service.APIKey),
		byID: make(map[int64]*service.APIKey),
	}
}

func (r *e2eAPIKeyRepo) add(k *service.APIKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[k.Key] = k
	r.byID[k.ID] = k
}

func cloneAPIKeyForE2E(k *service.APIKey) *service.APIKey {
	if k == nil {
		return nil
	}
	cp := *k
	if k.User != nil {
		u := *k.User
		cp.User = &u
	}
	if k.Group != nil {
		g := *k.Group
		if len(k.Group.ModelAllowlist.Models) > 0 {
			g.ModelAllowlist.Models = append([]string(nil), k.Group.ModelAllowlist.Models...)
		}
		cp.Group = &g
	}
	return &cp
}

func (r *e2eAPIKeyRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	r.mu.RLock()
	hook := r.onGetByKeyAuth
	k := r.keys[key]
	r.mu.RUnlock()
	if hook != nil {
		return hook(ctx, key)
	}
	if k == nil {
		return nil, service.ErrAPIKeyNotFound
	}
	return cloneAPIKeyForE2E(k), nil
}

func (r *e2eAPIKeyRepo) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKeyForAuth(ctx, key)
}

func (r *e2eAPIKeyRepo) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k := r.byID[id]
	if k == nil {
		return nil, service.ErrAPIKeyNotFound
	}
	return cloneAPIKeyForE2E(k), nil
}

func (r *e2eAPIKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func (r *e2eAPIKeyRepo) GetRateLimitData(context.Context, int64) (*service.APIKeyRateLimitData, error) {
	return &service.APIKeyRateLimitData{}, nil
}

type e2eUserRepo struct {
	service.UserRepository
	mu    sync.RWMutex
	users map[int64]*service.User
}

func newE2EUserRepo() *e2eUserRepo {
	return &e2eUserRepo{
		users: make(map[int64]*service.User),
	}
}

func (r *e2eUserRepo) add(u *service.User) {
	if u == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *u
	r.users[u.ID] = &cp
}

func (r *e2eUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u := r.users[id]
	if u == nil {
		return nil, service.ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *e2eUserRepo) GetByIDIncludeDeleted(ctx context.Context, id int64) (*service.User, error) {
	return r.GetByID(ctx, id)
}

func (r *e2eUserRepo) DeductBalance(context.Context, int64, float64) error { return nil }
func (r *e2eUserRepo) UpdateBalance(context.Context, int64, float64) error { return nil }
func (r *e2eUserRepo) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	return nil
}

type e2eAccountRepo struct {
	service.AccountRepository
	mu                sync.Mutex
	accounts          []service.Account
	onListSchedulable func(ctx context.Context, platform string) ([]service.Account, error)
}

func (r *e2eAccountRepo) setAccounts(accs ...service.Account) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts = append([]service.Account(nil), accs...)
}

func (r *e2eAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.mu.Lock()
	hook := r.onListSchedulable
	accs := append([]service.Account(nil), r.accounts...)
	r.mu.Unlock()
	if hook != nil {
		return hook(ctx, platform)
	}
	var out []service.Account
	for _, a := range accs {
		if a.Platform == platform && a.IsSchedulable() {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *e2eAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *e2eAccountRepo) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]service.Account, error) {
	var out []service.Account
	for _, p := range platforms {
		accs, err := r.ListSchedulableByPlatform(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, accs...)
	}
	return out, nil
}

func (r *e2eAccountRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, _ int64, platforms []string) ([]service.Account, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

func (r *e2eAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *e2eAccountRepo) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]service.Account, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

func (r *e2eAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	platSet := make(map[string]bool, len(platforms))
	for _, p := range platforms {
		platSet[p] = true
	}
	var out []service.Account
	for _, a := range r.accounts {
		if (len(platSet) == 0 || platSet[a.Platform]) && a.Status == service.StatusActive && a.Schedulable {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *e2eAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.accounts {
		if a.ID == id {
			cp := a
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *e2eAccountRepo) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []*service.Account
	for _, a := range r.accounts {
		if want[a.ID] {
			cp := a
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *e2eAccountRepo) UpdateLastUsed(context.Context, int64) error                  { return nil }
func (r *e2eAccountRepo) SetError(context.Context, int64, string) error              { return nil }
func (r *e2eAccountRepo) ClearError(context.Context, int64) error                    { return nil }
func (r *e2eAccountRepo) SetSchedulable(context.Context, int64, bool) error          { return nil }
func (r *e2eAccountRepo) SetRateLimited(context.Context, int64, time.Time) error     { return nil }
func (r *e2eAccountRepo) SetOverloaded(context.Context, int64, time.Time) error      { return nil }
func (r *e2eAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (r *e2eAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}
func (r *e2eAccountRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}

type e2eUsageLogRepo struct {
	service.UsageLogRepository
}

func (r *e2eUsageLogRepo) Create(context.Context, *service.UsageLog) (bool, error) {
	return true, nil
}

type e2eSettingRepo struct {
	service.SettingRepository
}

func (r *e2eSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

func (r *e2eSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type e2eConcurrencyCache struct {
	mu                sync.Mutex
	rejectUserSlot    bool
	rejectAccountSlot bool
	allowUserWait     bool
	wsLeases          map[int64]map[string]bool
	onAcquireUserSlot func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
}

func newE2EConcurrencyCache() *e2eConcurrencyCache {
	return &e2eConcurrencyCache{
		wsLeases: make(map[int64]map[string]bool),
	}
}

func (c *e2eConcurrencyCache) AcquireAccountSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	reject := c.rejectAccountSlot
	c.mu.Unlock()
	return !reject, nil
}
func (c *e2eConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error { return nil }
func (c *e2eConcurrencyCache) GetAccountConcurrency(context.Context, int64) (int, error) {
	return 0, nil
}
func (c *e2eConcurrencyCache) GetAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = 0
	}
	return out, nil
}
func (c *e2eConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (c *e2eConcurrencyCache) DecrementAccountWaitCount(context.Context, int64) error { return nil }
func (c *e2eConcurrencyCache) GetAccountWaitingCount(context.Context, int64) (int, error) {
	return 0, nil
}
func (c *e2eConcurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	c.mu.Lock()
	hook := c.onAcquireUserSlot
	reject := c.rejectUserSlot
	c.mu.Unlock()
	if hook != nil {
		return hook(ctx, userID, maxConcurrency, requestID)
	}
	return !reject, nil
}
func (c *e2eConcurrencyCache) ReleaseUserSlot(context.Context, int64, string) error { return nil }
func (c *e2eConcurrencyCache) GetUserConcurrency(context.Context, int64) (int, error) {
	return 0, nil
}
func (c *e2eConcurrencyCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	c.mu.Lock()
	allow := c.allowUserWait
	c.mu.Unlock()
	return allow, nil
}
func (c *e2eConcurrencyCache) DecrementWaitCount(context.Context, int64) error { return nil }
func (c *e2eConcurrencyCache) GetAccountsLoadBatch(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return map[int64]*service.AccountLoadInfo{}, nil
}
func (c *e2eConcurrencyCache) GetUsersLoadBatch(context.Context, []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	return map[int64]*service.UserLoadInfo{}, nil
}
func (c *e2eConcurrencyCache) CleanupExpiredAccountSlots(context.Context, int64) error { return nil }
func (c *e2eConcurrencyCache) CleanupExpiredAccountSlotKeys(context.Context) error       { return nil }
func (c *e2eConcurrencyCache) CleanupStaleProcessSlots(context.Context, string) error  { return nil }

func (c *e2eConcurrencyCache) AcquireOpenAIWSIngressLease(_ context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsLeases[apiKeyID] == nil {
		c.wsLeases[apiKeyID] = make(map[string]bool)
	}
	if maxConnections > 0 && len(c.wsLeases[apiKeyID]) >= maxConnections {
		return false, nil
	}
	c.wsLeases[apiKeyID][leaseID] = true
	return true, nil
}

func (c *e2eConcurrencyCache) RefreshOpenAIWSIngressLease(_ context.Context, apiKeyID int64, leaseID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wsLeases[apiKeyID][leaseID], nil
}

func (c *e2eConcurrencyCache) ReleaseOpenAIWSIngressLease(_ context.Context, apiKeyID int64, leaseID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.wsLeases[apiKeyID], leaseID)
	return nil
}

// ---------------------------------------------------------------------------
// Stdlib Prometheus Text Exposition Parser (no extra go.mod imports)
// ---------------------------------------------------------------------------

type promSample struct {
	name   string
	labels map[string]string
	value  float64
}

type promSnapshot struct {
	raw     string
	samples []promSample
}

func parsePrometheusText(raw string) (*promSnapshot, error) {
	snap := &promSnapshot{raw: raw}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sample, err := parsePrometheusLine(line)
		if err != nil {
			return nil, fmt.Errorf("parse metric line %q: %w", line, err)
		}
		snap.samples = append(snap.samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return snap, nil
}

func parsePrometheusLine(line string) (promSample, error) {
	labels := make(map[string]string)
	var name, rest string
	if idx := strings.IndexByte(line, '{'); idx >= 0 {
		name = strings.TrimSpace(line[:idx])
		end := findPromLabelEnd(line, idx+1)
		if end < 0 {
			return promSample{}, fmt.Errorf("unterminated labels")
		}
		if err := parsePromLabels(line[idx+1:end], labels); err != nil {
			return promSample{}, err
		}
		rest = strings.TrimSpace(line[end+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return promSample{}, fmt.Errorf("missing value")
		}
		name = fields[0]
		rest = strings.Join(fields[1:], " ")
	}
	valFields := strings.Fields(rest)
	if len(valFields) == 0 {
		return promSample{}, fmt.Errorf("missing value")
	}
	val, err := strconv.ParseFloat(valFields[0], 64)
	if err != nil {
		return promSample{}, fmt.Errorf("invalid float %q: %w", valFields[0], err)
	}
	return promSample{name: name, labels: labels, value: val}, nil
}

func findPromLabelEnd(s string, start int) int {
	inQuote := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuote {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if ch == '}' && !inQuote {
			return i
		}
	}
	return -1
}

func parsePromLabels(raw string, out map[string]string) error {
	i := 0
	for i < len(raw) {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == ',' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			break
		}
		eq := strings.IndexByte(raw[i:], '=')
		if eq < 0 {
			return fmt.Errorf("missing '=' in labels %q", raw[i:])
		}
		key := strings.TrimSpace(raw[i : i+eq])
		i += eq + 1
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		if i >= len(raw) || raw[i] != '"' {
			return fmt.Errorf("expected '\"' for label %s", key)
		}
		i++
		var val strings.Builder
		escaped := false
		closed := false
		for i < len(raw) {
			ch := raw[i]
			i++
			if escaped {
				switch ch {
				case 'n':
					val.WriteByte('\n')
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				default:
					val.WriteByte(ch)
				}
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				closed = true
				break
			}
			val.WriteByte(ch)
		}
		if !closed {
			return fmt.Errorf("unclosed quote for label %s", key)
		}
		out[key] = val.String()
	}
	return nil
}

func (s *promSnapshot) sum(metricName string, matchLabels ...string) float64 {
	total := 0.0
	for _, sample := range s.samples {
		if sample.name != metricName {
			continue
		}
		if promLabelsMatch(sample.labels, matchLabels...) {
			total += sample.value
		}
	}
	return total
}

func (s *promSnapshot) value(metricName string, matchLabels ...string) (float64, bool) {
	found := false
	total := 0.0
	for _, sample := range s.samples {
		if sample.name != metricName {
			continue
		}
		if promLabelsMatch(sample.labels, matchLabels...) {
			total += sample.value
			found = true
		}
	}
	return total, found
}

func promLabelsMatch(have map[string]string, pairs ...string) bool {
	for i := 0; i+1 < len(pairs); i += 2 {
		if have[pairs[i]] != pairs[i+1] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Opaque-box Realtime Requests API Helpers
// ---------------------------------------------------------------------------

type e2eRealtimeItem map[string]any

func (it e2eRealtimeItem) str(key string) string {
	v, ok := it[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (it e2eRealtimeItem) hasKey(key string) bool {
	v, ok := it[key]
	return ok && v != nil
}

func (it e2eRealtimeItem) intVal(key string) (int, bool) {
	v, ok := it[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func (it e2eRealtimeItem) floatVal(key string) (float64, bool) {
	v, ok := it[key]
	if !ok || v == nil {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

type e2eRealtimeResponse struct {
	StatusCode     int
	Code           int
	Enabled        bool
	TotalInBuffer  int
	Summary        e2eRealtimeItem
	Items          []e2eRealtimeItem
	Models         []string
	FailureReasons []string
	RawData        map[string]any
}

// ---------------------------------------------------------------------------
// Harness Construction & Request Drivers
// ---------------------------------------------------------------------------

const (
	e2eKeyOpenAI            = "sk-e2e-openai-valid-key-0001"
	e2eKeyAnthropic         = "sk-e2e-anthropic-valid-key-0002"
	e2eKeyGrok              = "sk-e2e-grok-valid-key-0003"
	e2eKeyIPRestricted      = "sk-e2e-ip-restricted-key-0004"
	e2eKeyDisabledGroup     = "sk-e2e-disabled-group-key-0005"
	e2eKeyDeletedGroup      = "sk-e2e-deleted-group-key-0006"
	e2eKeyUngrouped         = "sk-e2e-ungrouped-key-0007"
	e2eKeyDisabled          = "sk-e2e-disabled-key-0008"
	e2eKeyInactiveUser      = "sk-e2e-inactive-user-key-0009"
	e2eKeyExpired           = "sk-e2e-expired-key-0010"
	e2eKeyZeroBalance       = "sk-e2e-zero-balance-key-0011"
	e2eKeyQuotaExhausted    = "sk-e2e-quota-exhausted-key-0012"
	e2eKeyModelAllowlist    = "sk-e2e-model-allowlist-key-0013"
)

type e2eHarnessConfig struct {
	maxBodySize         int64
	maxWSIngressPerKey  int
	pingIntervalSeconds int
	runMode             string
	customKeyMutator    func(*e2eAPIKeyRepo, *e2eAccountRepo, *e2eMockUpstream)
	compositeResolver   *service.CompositeRouteResolver
}

type e2eOpsHarness struct {
	t                *testing.T
	cfg              *config.Config
	router           *gin.Engine
	appServer        *httptest.Server
	metricsServer    *monitoring.Server
	upstream         *e2eMockUpstream
	openaiSvc        *service.OpenAIGatewayService
	apiKeyRepo       *e2eAPIKeyRepo
	accountRepo      *e2eAccountRepo
	concurrencyCache *e2eConcurrencyCache
	sampleWriter     *service.OpsSampleWriter
	httpClient       *http.Client
}

func newE2EOpsHarness(t *testing.T, mutators ...func(*e2eHarnessConfig)) *e2eOpsHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	hcfg := &e2eHarnessConfig{
		maxBodySize:        4096,
		maxWSIngressPerKey: 0,
		runMode:            config.RunModeStandard,
	}
	for _, fn := range mutators {
		if fn != nil {
			fn(hcfg)
		}
	}

	upstream := newE2EMockUpstream(t)

	cfg := &config.Config{
		RunMode: hcfg.runMode,
		Default: config.DefaultConfig{
			RateMultiplier: 1.0,
		},
		Concurrency: config.ConcurrencyConfig{
			PingInterval: hcfg.pingIntervalSeconds,
		},
		Gateway: config.GatewayConfig{
			MaxBodySize:                  hcfg.maxBodySize,
			TextMaxBodySize:              hcfg.maxBodySize,
			RequestBodyMemoryBudgetBytes: 8 * 1024 * 1024,
			MaxLineSize:                  1 << 20,
			ResponseHeaderTimeout:        3,
			OpenAIResponseHeaderTimeout:  3,
			MaxAccountSwitches:           3,
			OpenAIWS: config.GatewayOpenAIWSConfig{
				Enabled:                       true,
				APIKeyEnabled:                 true,
				ResponsesWebsocketsV2:         true,
				ModeRouterV2Enabled:           true,
				DialTimeoutSeconds:            3,
				ReadTimeoutSeconds:            3,
				WriteTimeoutSeconds:           3,
				MaxIngressConnectionsPerAPIKey: hcfg.maxWSIngressPerKey,
			},
		},
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowPrivateHosts: true,
				AllowInsecureHTTP: true,
			},
		},
		Monitoring: config.MonitoringConfig{
			MetricsEnabled:    true,
			MetricsBind:       "127.0.0.1:0",
			OpsMetricsEnabled: true,
			OpsSamplesEnabled: true,
		},
	}

	instruments, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
	require.NoError(t, err)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	sampleWriter := service.NewOpsSampleWriter(rdb, 200, 24)
	sampleWriter.Start()
	t.Cleanup(sampleWriter.Stop)

	runtime := monitoring.NewOpsRuntime(instruments.Ops)
	runtime.SampleSink = sampleWriter
	runtime.Accounts = monitoring.NewAccountLabels([]string{"openai-acc-1", "openai-acc-2", "anthropic-acc-1"})

	metricsSrv := monitoring.ProvideMetricsServer(cfg, instruments.Registry)
	require.NoError(t, metricsSrv.StartEnabled())
	t.Cleanup(func() { _ = metricsSrv.Stop(context.Background()) })

	apiKeyRepo := newE2EAPIKeyRepo()
	userRepo := newE2EUserRepo()
	accountRepo := &e2eAccountRepo{}
	seedE2EFixtures(apiKeyRepo, userRepo, accountRepo, upstream.URL())
	if hcfg.customKeyMutator != nil {
		hcfg.customKeyMutator(apiKeyRepo, accountRepo, upstream)
	}

	concurrencyCache := newE2EConcurrencyCache()
	concurrencySvc := service.NewConcurrencyService(concurrencyCache)
	settingSvc := service.NewSettingService(&e2eSettingRepo{}, cfg)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	billingSvc := service.NewBillingService(cfg, nil)
	billingCacheSvc := service.NewBillingCacheService(nil, userRepo, nil, apiKeyRepo, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	rateLimitSvc := service.NewRateLimitService(accountRepo, nil, cfg, nil, nil)
	httpUpstream := repository.NewHTTPUpstream(cfg)
	usageLogRepo := &e2eUsageLogRepo{}

	oaiSvc := service.NewOpenAIGatewayService(
		accountRepo, usageLogRepo, nil, userRepo, nil, nil, nil, cfg,
		nil, concurrencySvc, billingSvc, rateLimitSvc, billingCacheSvc,
		httpUpstream, &service.DeferredService{}, nil, nil, nil, nil, nil, settingSvc, nil,
	)
	gwSvc := service.NewGatewayService(
		accountRepo, nil, usageLogRepo, nil, userRepo, nil, nil, nil, cfg,
		nil, concurrencySvc, billingSvc, rateLimitSvc, billingCacheSvc, nil,
		httpUpstream, &service.DeferredService{}, nil, nil, nil, nil, settingSvc, nil, nil, nil, nil, nil, nil,
	)

	oaiHandler := handler.NewOpenAIGatewayHandler(oaiSvc, concurrencySvc, billingCacheSvc, apiKeySvc, nil, nil, nil, nil, cfg)
	gwHandler := handler.NewGatewayHandler(gwSvc, oaiSvc, nil, nil, nil, concurrencySvc, billingCacheSvc, nil, apiKeySvc, nil, nil, nil, nil, cfg, settingSvc)
	opsHandler := adminhandler.NewOpsHandler(nil)
	opsHandler.SetRealtimeRequestsReader(sampleWriter)

	handlers := &handler.Handlers{
		Gateway:       gwHandler,
		OpenAIGateway: oaiHandler,
		AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		Admin: &handler.AdminHandlers{
			Ops: opsHandler,
		},
	}

	router := gin.New()
	installObservability(router, cfg, middleware.ProvideMetricsMiddleware(cfg, instruments.Metrics), runtime)
	router.Use(middleware.RequestLogger())

	apiKeyAuth := middleware.NewAPIKeyAuthMiddleware(apiKeySvc, nil, cfg)
	routes.RegisterGatewayRoutes(router, handlers, apiKeyAuth, apiKeySvc, nil, nil, settingSvc, hcfg.compositeResolver, cfg)

	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() })
	auditLog := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	routes.RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil)

	appServer := httptest.NewServer(router)
	t.Cleanup(appServer.Close)
	t.Cleanup(func() { middleware.SetIngressRejectRecorder(nil) })

	return &e2eOpsHarness{
		t:                t,
		cfg:              cfg,
		router:           router,
		appServer:        appServer,
		metricsServer:    metricsSrv,
		upstream:         upstream,
		openaiSvc:        oaiSvc,
		apiKeyRepo:       apiKeyRepo,
		accountRepo:      accountRepo,
		concurrencyCache: concurrencyCache,
		sampleWriter:     sampleWriter,
		httpClient:       &http.Client{Timeout: 10 * time.Second},
	}
}

func seedE2EFixtures(keyRepo *e2eAPIKeyRepo, userRepo *e2eUserRepo, accRepo *e2eAccountRepo, upstreamURL string) {
	openAIGroupID := int64(2001)
	anthropicGroupID := int64(2002)
	grokGroupID := int64(2003)
	disabledGroupID := int64(2004)
	deletedGroupID := int64(2005)
	allowlistGroupID := int64(2006)

	openAIGroup := &service.Group{
		ID:               openAIGroupID,
		Name:             "openai-std-group",
		Platform:         service.PlatformOpenAI,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
		Hydrated:         true,
	}
	anthropicGroup := &service.Group{
		ID:               anthropicGroupID,
		Name:             "anthropic-std-group",
		Platform:         service.PlatformAnthropic,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
		Hydrated:         true,
	}
	grokGroup := &service.Group{
		ID:               grokGroupID,
		Name:             "grok-std-group",
		Platform:         service.PlatformGrok,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1.0,
		Hydrated:         true,
	}
	disabledGroup := &service.Group{
		ID:               disabledGroupID,
		Name:             "disabled-group",
		Platform:         service.PlatformOpenAI,
		Status:           service.StatusDisabled,
		SubscriptionType: service.SubscriptionTypeStandard,
		Hydrated:         true,
	}
	deletedGroup := &service.Group{
		ID:               deletedGroupID,
		Name:             "deleted-group",
		Platform:         service.PlatformOpenAI,
		Status:           "deleted",
		SubscriptionType: service.SubscriptionTypeStandard,
		Hydrated:         true,
	}
	allowlistGroup := &service.Group{
		ID:               allowlistGroupID,
		Name:             "allowlist-group",
		Platform:         service.PlatformOpenAI,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		ModelAllowlist:   service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-4o", "gpt-5.1"}},
		Hydrated:         true,
	}

	alice := &service.User{ID: 1001, Email: "alice@e2e.test", Role: service.RoleUser, Status: service.StatusActive, Balance: 1000, Concurrency: 5}
	bob := &service.User{ID: 1002, Email: "bob@e2e.test", Role: service.RoleUser, Status: service.StatusActive, Balance: 1000, Concurrency: 5}
	grokUser := &service.User{ID: 1003, Email: "grok@e2e.test", Role: service.RoleUser, Status: service.StatusActive, Balance: 1000, Concurrency: 5}
	inactiveUser := &service.User{ID: 1004, Email: "inactive@e2e.test", Role: service.RoleUser, Status: service.StatusDisabled, Balance: 1000, Concurrency: 5}
	zeroBalUser := &service.User{ID: 1005, Email: "zerobal@e2e.test", Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 5}
	userRepo.add(alice)
	userRepo.add(bob)
	userRepo.add(grokUser)
	userRepo.add(inactiveUser)
	userRepo.add(zeroBalUser)

	past := time.Now().Add(-24 * time.Hour)

	keyRepo.add(&service.APIKey{ID: 1101, UserID: alice.ID, Name: "openai-main-key", Key: e2eKeyOpenAI, Status: service.StatusActive, User: alice, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1102, UserID: bob.ID, Name: "anthropic-main-key", Key: e2eKeyAnthropic, Status: service.StatusActive, User: bob, GroupID: &anthropicGroupID, Group: anthropicGroup})
	keyRepo.add(&service.APIKey{ID: 1103, UserID: grokUser.ID, Name: "grok-main-key", Key: e2eKeyGrok, Status: service.StatusActive, User: grokUser, GroupID: &grokGroupID, Group: grokGroup})
	keyRepo.add(&service.APIKey{ID: 1104, UserID: alice.ID, Name: "ip-restricted-key", Key: e2eKeyIPRestricted, Status: service.StatusActive, IPWhitelist: []string{"10.0.0.0/8"}, User: alice, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1105, UserID: alice.ID, Name: "disabled-grp-key", Key: e2eKeyDisabledGroup, Status: service.StatusActive, User: alice, GroupID: &disabledGroupID, Group: disabledGroup})
	keyRepo.add(&service.APIKey{ID: 1106, UserID: alice.ID, Name: "deleted-grp-key", Key: e2eKeyDeletedGroup, Status: service.StatusActive, User: alice, GroupID: &deletedGroupID, Group: deletedGroup})
	keyRepo.add(&service.APIKey{ID: 1107, UserID: alice.ID, Name: "ungrouped-key", Key: e2eKeyUngrouped, Status: service.StatusActive, User: alice, GroupID: nil, Group: nil})
	keyRepo.add(&service.APIKey{ID: 1108, UserID: alice.ID, Name: "disabled-key", Key: e2eKeyDisabled, Status: service.StatusDisabled, User: alice, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1109, UserID: inactiveUser.ID, Name: "inactive-user-key", Key: e2eKeyInactiveUser, Status: service.StatusActive, User: inactiveUser, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1110, UserID: alice.ID, Name: "expired-key", Key: e2eKeyExpired, Status: service.StatusActive, ExpiresAt: &past, User: alice, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1111, UserID: zeroBalUser.ID, Name: "zero-bal-key", Key: e2eKeyZeroBalance, Status: service.StatusActive, User: zeroBalUser, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1112, UserID: alice.ID, Name: "quota-exhausted-key", Key: e2eKeyQuotaExhausted, Status: service.StatusAPIKeyQuotaExhausted, Quota: 10, QuotaUsed: 10, User: alice, GroupID: &openAIGroupID, Group: openAIGroup})
	keyRepo.add(&service.APIKey{ID: 1113, UserID: alice.ID, Name: "allowlist-key", Key: e2eKeyModelAllowlist, Status: service.StatusActive, User: alice, GroupID: &allowlistGroupID, Group: allowlistGroup})

	accRepo.setAccounts(
		service.Account{
			ID:          3001,
			Name:        "openai-acc-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    1,
			Credentials: map[string]any{"api_key": "sk-upstream-openai-1", "base_url": upstreamURL},
			Extra: map[string]any{
				"openai_responses_supported":                    true,
				"openai_responses_mode":                         "auto",
				"openai_apikey_responses_websockets_v2_enabled": true,
				"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			},
		},
		service.Account{
			ID:          3002,
			Name:        "openai-acc-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    2,
			Credentials: map[string]any{"api_key": "sk-upstream-openai-2", "base_url": upstreamURL},
			Extra: map[string]any{
				"openai_responses_supported":                    true,
				"openai_responses_mode":                         "auto",
				"openai_apikey_responses_websockets_v2_enabled": true,
				"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			},
		},
		service.Account{
			ID:          3003,
			Name:        "anthropic-acc-1",
			Platform:    service.PlatformAnthropic,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    1,
			Credentials: map[string]any{"api_key": "sk-ant-upstream-1", "base_url": upstreamURL},
		},
	)
}

func (h *e2eOpsHarness) doHTTP(method, path, apiKey, body string, extraHeaders ...map[string]string) (int, http.Header, []byte, string) {
	h.t.Helper()
	reqID := uuid.NewString()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.appServer.URL+path, bodyReader)
	require.NoError(h.t, err)
	req.Header.Set("X-Request-Id", reqID)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for _, hdrs := range extraHeaders {
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
	}
	resp, err := h.httpClient.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	if gotID := strings.TrimSpace(resp.Header.Get("X-Request-Id")); gotID != "" {
		reqID = gotID
	}
	return resp.StatusCode, resp.Header.Clone(), raw, reqID
}

func (h *e2eOpsHarness) dialWS(path, apiKey string, extraHeaders ...http.Header) (*coderws.Conn, *http.Response, string, error) {
	h.t.Helper()
	reqID := uuid.NewString()
	hdr := http.Header{}
	if len(extraHeaders) > 0 && extraHeaders[0] != nil {
		hdr = extraHeaders[0].Clone()
	}
	hdr.Set("X-Request-Id", reqID)
	if apiKey != "" {
		hdr.Set("Authorization", "Bearer "+apiKey)
	}
	wsURL := "ws" + strings.TrimPrefix(h.appServer.URL, "http") + path
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, resp, err := coderws.Dial(ctx, wsURL, &coderws.DialOptions{
		HTTPHeader:      hdr,
		CompressionMode: coderws.CompressionContextTakeover,
	})
	return conn, resp, reqID, err
}

func (h *e2eOpsHarness) runHappyWSRoundTrip(path, apiKey string) string {
	h.t.Helper()
	conn, _, reqID, err := h.dialWS(path, apiKey)
	require.NoError(h.t, err)
	defer func() { _ = conn.CloseNow() }()

	wctx, wcancel := context.WithTimeout(context.Background(), 3*time.Second)
	err = conn.Write(wctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-4o","input":"ping"}`))
	wcancel()
	require.NoError(h.t, err)

	rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, frame, err := conn.Read(rctx)
	rcancel()
	require.NoError(h.t, err)
	require.Contains(h.t, string(frame), `"response.completed"`)

	require.NoError(h.t, conn.Close(coderws.StatusNormalClosure, "done"))
	return reqID
}

func (h *e2eOpsHarness) scrapeMetrics() *promSnapshot {
	h.t.Helper()
	resp, err := h.httpClient.Get("http://" + h.metricsServer.Addr() + "/metrics")
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(h.t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	snap, err := parsePrometheusText(string(raw))
	require.NoError(h.t, err)
	return snap
}

func (h *e2eOpsHarness) queryRealtime(q url.Values) e2eRealtimeResponse {
	h.t.Helper()
	u := h.appServer.URL + "/api/v1/admin/ops/realtime-requests"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := h.httpClient.Get(u)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	require.NoError(h.t, json.Unmarshal(raw, &envelope), "raw body: %s", string(raw))

	out := e2eRealtimeResponse{
		StatusCode: resp.StatusCode,
		Code:       envelope.Code,
		RawData:    envelope.Data,
	}
	if envelope.Data == nil {
		return out
	}
	if v, ok := envelope.Data["enabled"].(bool); ok {
		out.Enabled = v
	}
	if v, ok := envelope.Data["total_in_buffer"].(float64); ok {
		out.TotalInBuffer = int(v)
	}
	if s, ok := envelope.Data["summary"].(map[string]any); ok {
		out.Summary = e2eRealtimeItem(s)
	}
	if rawItems, ok := envelope.Data["items"].([]any); ok {
		for _, elem := range rawItems {
			if m, ok := elem.(map[string]any); ok {
				out.Items = append(out.Items, e2eRealtimeItem(m))
			}
		}
	}
	if rawModels, ok := envelope.Data["models"].([]any); ok {
		for _, elem := range rawModels {
			if s, ok := elem.(string); ok {
				out.Models = append(out.Models, s)
			}
		}
	}
	if rawReasons, ok := envelope.Data["failure_reasons"].([]any); ok {
		for _, elem := range rawReasons {
			if s, ok := elem.(string); ok {
				out.FailureReasons = append(out.FailureReasons, s)
			}
		}
	}
	return out
}

func (h *e2eOpsHarness) waitForSample(requestID string) e2eRealtimeItem {
	h.t.Helper()
	var found e2eRealtimeItem
	require.Eventually(h.t, func() bool {
		q := url.Values{}
		q.Set("limit", "200")
		q.Set("search", requestID)
		res := h.queryRealtime(q)
		for _, it := range res.Items {
			if it.str("request_id") == requestID {
				found = it
				return true
			}
		}
		return false
	}, 4*time.Second, 50*time.Millisecond, "timed out waiting for OpsSample request_id=%s", requestID)
	return found
}

func (h *e2eOpsHarness) sendSentinelAndAssertAbsent(absentRequestID string) {
	h.t.Helper()
	status, _, _, sentinelID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"sentinel"}`)
	require.Equal(h.t, http.StatusOK, status)
	_ = h.waitForSample(sentinelID)

	q := url.Values{}
	q.Set("limit", "200")
	res := h.queryRealtime(q)
	for _, it := range res.Items {
		require.NotEqual(h.t, absentRequestID, it.str("request_id"), "WS request_id %s must NOT appear in realtime-requests", absentRequestID)
	}
}

func assertSampleAuthMetaPopulated(t *testing.T, item e2eRealtimeItem, wantEmail, wantKeyName, wantGroupName string) {
	t.Helper()
	uid, ok := item.intVal("user_id")
	require.True(t, ok && uid > 0, "user_id should be populated, got %#v", item["user_id"])
	require.Equal(t, wantEmail, item.str("user_email"))
	kid, ok := item.intVal("api_key_id")
	require.True(t, ok && kid > 0, "api_key_id should be populated, got %#v", item["api_key_id"])
	require.Equal(t, wantKeyName, item.str("api_key_name"))
	require.NotEmpty(t, item.str("api_key_masked"))
	if wantGroupName != "" {
		gid, ok := item.intVal("group_id")
		require.True(t, ok && gid > 0, "group_id should be populated, got %#v", item["group_id"])
		require.Equal(t, wantGroupName, item.str("group_name"))
	}
}

// ---------------------------------------------------------------------------
// TIER 1: Core Feature Coverage (F1–F9, >=5 subtests each = 46 subtests)
// ---------------------------------------------------------------------------

func TestE2E_OpsMonitoring_T1_FeatureCoverage(t *testing.T) {
	// ---- F1: Exclude WS upgrades from isOpsGatewayRoute ----
	t.Run("F1_WSUpgrade_V1Responses_Excluded", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		wsReqID := h.runHappyWSRoundTrip("/v1/responses", e2eKeyOpenAI)
		h.sendSentinelAndAssertAbsent(wsReqID)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"), "only sentinel HTTP request must count as ops-eligible")
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total"), "only sentinel HTTP request must emit gateway_requests_total")
	})

	t.Run("F1_WSUpgrade_RootResponses_Excluded", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		wsReqID := h.runHappyWSRoundTrip("/responses", e2eKeyOpenAI)
		h.sendSentinelAndAssertAbsent(wsReqID)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total"))
	})

	t.Run("F1_WSUpgrade_CodexResponses_Excluded", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		wsReqID := h.runHappyWSRoundTrip("/backend-api/codex/responses", e2eKeyOpenAI)
		h.sendSentinelAndAssertAbsent(wsReqID)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total"))
	})

	t.Run("F1_NonUpgrade_GET_V1Responses_IsEligible", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "")
		require.Equal(t, http.StatusUpgradeRequired, status)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total"))

		item := h.waitForSample(reqID)
		require.Equal(t, "GET", item.str("http_method"))
		gotStatus, ok := item.intVal("http_status")
		require.True(t, ok)
		require.Equal(t, http.StatusUpgradeRequired, gotStatus)
	})

	t.Run("F1_HTTP_POST_V1Responses_EligibleParity", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hello"}`)
		require.Equal(t, http.StatusOK, status)
		_ = h.waitForSample(reqID)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total", "result", "success", "failure_reason", ""))
		// 6 exclusive phases on sub2api_request_phase_seconds_count
		for _, phase := range []string{"body_read", "wait", "gateway", "upstream", "response", "finish"} {
			require.Equal(t, 1.0, snap.sum("sub2api_request_phase_seconds_count", "phase", phase), "phase=%s", phase)
		}
	})

	// ---- F2: HTTP 101 requestResult success ----
	t.Run("F2_WSUpgrade_V1Responses_NeverGatewayErrorUnknown", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_ = h.runHappyWSRoundTrip("/v1/responses", e2eKeyOpenAI)
		snap := h.scrapeMetrics()
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error", "failure_reason", "unknown"))
		require.Zero(t, snap.sum("sub2api_gateway_requests_total"))
	})

	t.Run("F2_WSUpgrade_CodexResponses_NeverGatewayErrorUnknown", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_ = h.runHappyWSRoundTrip("/backend-api/codex/responses", e2eKeyOpenAI)
		snap := h.scrapeMetrics()
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})

	t.Run("F2_EligibleRoute_101Status_ClassifiedSuccess", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		// Dedicated router using the same observability chain + an eligible route returning 101.
		r101 := gin.New()
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		sw := service.NewOpsSampleWriter(rdb, 50, 24)
		sw.Start()
		t.Cleanup(sw.Stop)

		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		rt := monitoring.NewOpsRuntime(inst.Ops)
		rt.SampleSink = sw
		installObservability(r101, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), rt)
		r101.Use(middleware.RequestLogger())
		r101.GET("/v1/images/batches", func(c *gin.Context) {
			c.Status(http.StatusSwitchingProtocols)
		})
		adminOps := adminhandler.NewOpsHandler(nil)
		adminOps.SetRealtimeRequestsReader(sw)
		r101.GET("/api/v1/admin/ops/realtime-requests", adminOps.GetRealtimeRequests)

		srv := httptest.NewServer(r101)
		t.Cleanup(srv.Close)

		reqID := uuid.NewString()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/images/batches", nil)
		require.NoError(t, err)
		req.Header.Set("X-Request-Id", reqID)
		w := httptest.NewRecorder()
		r101.ServeHTTP(w, req)
		require.Equal(t, http.StatusSwitchingProtocols, w.Code)

		families, err := inst.Registry.Gather()
		require.NoError(t, err)
		var successCount, gwErrCount float64
		for _, f := range families {
			if f.GetName() == "sub2api_gateway_requests_total" {
				for _, m := range f.GetMetric() {
					var res, reason string
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "result" {
							res = lp.GetValue()
						}
						if lp.GetName() == "failure_reason" {
							reason = lp.GetValue()
						}
					}
					if res == "success" && reason == "" {
						successCount += m.GetCounter().GetValue()
					}
					if res == "gateway_error" {
						gwErrCount += m.GetCounter().GetValue()
					}
				}
			}
		}
		require.Equal(t, 1.0, successCount)
		require.Zero(t, gwErrCount)

		require.Eventually(t, func() bool {
			resp, err := h.httpClient.Get(srv.URL + "/api/v1/admin/ops/realtime-requests?search=" + reqID)
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			var env struct {
				Data struct {
					Items []map[string]any `json:"items"`
				} `json:"data"`
			}
			if json.NewDecoder(resp.Body).Decode(&env) != nil {
				return false
			}
			for _, it := range env.Data.Items {
				if it["request_id"] == reqID {
					require.Equal(t, "success", it["result"])
					require.Equal(t, float64(101), it["http_status"])
					require.Empty(t, it["failure_reason"])
					return true
				}
			}
			return false
		}, 3*time.Second, 50*time.Millisecond)
	})

	t.Run("F2_EligibleRoute_101Status_ExcludedFromFailures", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		sw := service.NewOpsSampleWriter(rdb, 50, 24)
		sw.Start()
		t.Cleanup(sw.Stop)

		rt := monitoring.NewOpsRuntime(inst.Ops)
		rt.SampleSink = sw
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), rt)
		r.GET("/v1/images/batches", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/images/batches", nil))

		require.Eventually(t, func() bool {
			res, err := sw.ListRealtimeRequests(context.Background(), service.RealtimeRequestsFilter{Result: "all", Limit: 50})
			return err == nil && res.Summary.Total == 1
		}, 3*time.Second, 50*time.Millisecond)

		failRes, err := sw.ListRealtimeRequests(context.Background(), service.RealtimeRequestsFilter{Result: "failure", Limit: 50})
		require.NoError(t, err)
		require.Empty(t, failRes.Items)
		require.Zero(t, failRes.Summary.Failure)
	})

	t.Run("F2_WSUpgrade_EarlyClientClose_No101FalseFailure", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, wsReqID, err := h.dialWS("/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		require.NoError(h.t, conn.Close(coderws.StatusNormalClosure, "bye"))
		h.sendSentinelAndAssertAbsent(wsReqID)

		snap := h.scrapeMetrics()
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
		q := url.Values{}
		q.Set("result", "failure")
		res := h.queryRealtime(q)
		require.Empty(t, res.Items)
	})

	// ---- F3: Separate WS Prometheus metrics ----
	t.Run("F3_ResponsesWS_ActiveGaugeAndConnectedClosedNormal", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)

		// While WS connection is open, active gauge must be 1 and connected >= 1.
		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 1 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "connected") == 1
		}, 3*time.Second, 25*time.Millisecond, "expected active=1 and connected=1 while WS is open")

		wctx, wcancel := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, conn.Write(wctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-4o","input":"hi"}`)))
		wcancel()
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _, err = conn.Read(rctx)
		rcancel()
		require.NoError(t, err)
		require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal") == 1
		}, 3*time.Second, 25*time.Millisecond, "expected active=0 and closed_normal=1 after WS close")

		snap := h.scrapeMetrics()
		require.Zero(t, snap.sum("sub2api_request_phase_seconds_count"), "WS session must not touch HTTP 6-phase metrics")
	})

	t.Run("F3_CodexResponsesWS_UsesResponsesEndpointLabel", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_ = h.runHappyWSRoundTrip("/backend-api/codex/responses", e2eKeyOpenAI)
		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "connected") == 1 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal") == 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("F3_ResponsesWS_RejectedOnIngressCapacity", func(t *testing.T) {
		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.maxWSIngressPerKey = 1
		})
		conn1, _, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = conn1.CloseNow() }()

		_, resp2, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.Error(t, err)
		require.NotNil(t, resp2)
		require.Equal(t, http.StatusTooManyRequests, resp2.StatusCode)

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "rejected") == 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 1
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("F3_ResponsesWS_HandshakeFailedOnMalformedUpgrade", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		// Send Upgrade: websocket + Connection: Upgrade without Sec-WebSocket-Key so coderws.Accept fails.
		status, _, _, _ := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":    "websocket",
			"Connection": "Upgrade",
		})
		require.Equal(t, http.StatusBadRequest, status)

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "handshake_failed") == 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("F3_GrokRealtime_RejectedIncrementsRealtimeEndpoint", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		// No Grok account seeded -> pre-accept 503 rejection on GET /v1/realtime.
		_, resp, _, err := h.dialWS("/v1/realtime?model=grok-voice-latest", e2eKeyGrok)
		require.Error(t, err)
		require.NotNil(t, resp)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "realtime", "outcome", "rejected") == 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "realtime") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	// ---- F4: Early auth metadata on rejections ----
	t.Run("F4_IPRestriction403_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "ip-restricted-key", "openai-std-group")
	})

	t.Run("F4_DisabledGroup403_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyDisabledGroup, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "disabled-grp-key", "disabled-group")
	})

	t.Run("F4_ModelAllowlist404_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-forbidden","input":"hi"}`)
		require.Equal(t, http.StatusNotFound, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "allowlist-key", "allowlist-group")
	})

	t.Run("F4_ConflictingFields400_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","model":"gpt-4.1","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "openai-main-key", "openai-std-group")
	})

	t.Run("F4_QuotaExhausted429_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyQuotaExhausted, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "quota-exhausted-key", "openai-std-group")
	})

	// ---- F5: OpsSample troubleshooting fields & redaction ----
	t.Run("F5_HTTPMethod_PopulatedOnSuccessAndFailure", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		st1, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"ok"}`)
		require.Equal(t, http.StatusOK, st1)
		it1 := h.waitForSample(id1)
		require.Equal(t, "POST", it1.str("http_method"))

		st2, _, _, id2 := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "")
		require.Equal(t, http.StatusUpgradeRequired, st2)
		it2 := h.waitForSample(id2)
		require.Equal(t, "GET", it2.str("http_method"))
	})

	t.Run("F5_UpstreamStatusAndErrorCode_On429", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status:  http.StatusTooManyRequests,
			Headers: map[string]string{"Retry-After": "2"},
			Body:    `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit hit on model gpt-4o"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, status)

		item := h.waitForSample(reqID)
		upStatus, ok := item.intVal("upstream_status")
		require.True(t, ok, "upstream_status must be set")
		require.Equal(t, 429, upStatus)
		require.Equal(t, "rate_limit_exceeded", item.str("upstream_error_code"))
		require.Contains(t, item.str("error_message"), "Rate limit hit")
	})

	t.Run("F5_UpstreamStatusAndErrorCode_On400", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   `{"error":{"type":"invalid_request_error","code":"invalid_model","message":"The requested model does not exist"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)

		item := h.waitForSample(reqID)
		upStatus, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 400, upStatus)
		require.Equal(t, "invalid_model", item.str("upstream_error_code"))
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("F5_SecretRedaction_InErrorMessage", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   `{"error":{"type":"invalid_request_error","code":"bad_auth","message":"Invalid token sk-ant-api03-supersecretvalue12345 and Bearer eyJhbGciOiJIUzI1NiJ9.payloadsecret and https://api.test/v1?api_key=querysecret999"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)

		item := h.waitForSample(reqID)
		msg := item.str("error_message")
		require.NotEmpty(t, msg)
		require.NotContains(t, msg, "supersecretvalue12345")
		require.NotContains(t, msg, "payloadsecret")
		require.NotContains(t, msg, "querysecret999")
	})

	t.Run("F5_ErrorMessageTruncation_Within512Runes", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		longText := strings.Repeat("upstream_detail_", 60) // 960 chars
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"too_long","message":%q}}`, longText),
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)

		item := h.waitForSample(reqID)
		msg := item.str("error_message")
		runeLen := utf8.RuneCountInString(msg)
		require.GreaterOrEqual(t, runeLen, 256, "truncated error_message should retain useful context (256..512), got %d", runeLen)
		require.LessOrEqual(t, runeLen, 512, "error_message must be <= 512 runes, got %d", runeLen)
	})

	// ---- F6: 4xx failure_reason enum classification ----
	t.Run("F6_400_ClientBadRequest", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","model":"gpt-5","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_bad_request", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "client_bad_request"))
	})

	t.Run("F6_401_ClientAuthFailed", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", "sk-nonexistent-key-xyz", `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusUnauthorized, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_auth_failed", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "client_auth_failed"))
	})

	t.Run("F6_403_ClientForbidden", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_forbidden", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "client_forbidden"))
	})

	t.Run("F6_404_ClientNotFound", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"hi"}`)
		require.Equal(t, http.StatusNotFound, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_not_found", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "client_not_found"))
	})

	t.Run("F6_413_BodyTooLarge", func(t *testing.T) {
		h := newE2EOpsHarness(t) // MaxBodySize = 4096
		oversized := fmt.Sprintf(`{"model":"gpt-4o","input":%q}`, strings.Repeat("A", 8192))
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, oversized)
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "body_too_large", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "body_too_large"))
	})

	t.Run("F6_429_GatewayRatelimit", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyQuotaExhausted, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "gateway_ratelimit", item.str("failure_reason"))
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "gateway_ratelimit"))
	})

	// ---- F7: Middleware abort & panic error capture ----
	t.Run("F7_APIKeyAuthAbort_CapturesErrorMessage", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", "sk-invalid-key-f7", `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusUnauthorized, status)
		item := h.waitForSample(reqID)
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("F7_GroupModelAllowlistAbort_CapturesBlockedModel", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"claude-blocked-model","input":"hi"}`)
		require.Equal(t, http.StatusNotFound, status)
		item := h.waitForSample(reqID)
		require.Contains(t, item.str("error_message"), "claude-blocked-model")
	})

	t.Run("F7_RejectConflictingFieldsAbort_CapturesFieldName", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","model":"gpt-4.1","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		require.Contains(t, item.str("error_message"), "model")
	})

	t.Run("F7_RequestBodyLimitAbort_CapturesErrorMessage", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		oversized := fmt.Sprintf(`{"model":"gpt-4o","input":%q}`, strings.Repeat("B", 6000))
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, oversized)
		require.Equal(t, http.StatusRequestEntityTooLarge, status)
		item := h.waitForSample(reqID)
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("F7_RecoveryPanic_CapturesGatewayPanicAndRedactedMsg", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.onListSchedulable = func(context.Context, string) ([]service.Account, error) {
			panic("unexpected crash with Bearer sk-panicsecret1234567890")
		}
		h.accountRepo.mu.Unlock()

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusInternalServerError, status)

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error", "failure_reason", "gateway_panic"))

		item := h.waitForSample(reqID)
		require.Equal(t, "gateway_error", item.str("result"))
		require.Equal(t, "gateway_panic", item.str("failure_reason"))
		msg := item.str("error_message")
		require.NotEmpty(t, msg)
		require.NotContains(t, msg, "sk-panicsecret1234567890")
	})

	// ---- F8: Handler & service error/upstream/meta capture ----
	t.Run("F8_AnthropicMessages_Upstream500_CapturesMetaAndStatus", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusInternalServerError,
			Body:   `{"type":"error","error":{"type":"api_error","message":"Internal Anthropic outage"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		require.Equal(t, "POST", item.str("http_method"))
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_server_error", item.str("failure_reason"))
		upStatus, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 500, upStatus)
		require.Contains(t, item.str("error_message"), "Internal Anthropic outage")
		assertSampleAuthMetaPopulated(t, item, "bob@e2e.test", "anthropic-main-key", "anthropic-std-group")
	})

	t.Run("F8_OpenAIResponses_Upstream401_CapturesUpstreamAuth", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusUnauthorized,
			Body:   `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Upstream credential rejected"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_auth", item.str("failure_reason"))
		upStatus, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 401, upStatus)
		require.Equal(t, "invalid_api_key", item.str("upstream_error_code"))
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("F8_PreEarlyReturnCapture_WhenOpsServiceNil", func(t *testing.T) {
		// Base harness uses opsService=nil; OpsErrorLoggerMiddleware must still populate OpsRecorder before returning early.
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadGateway,
			Body:   `{"error":{"type":"server_error","code":"bad_gateway_upstream","message":"Upstream node restarted"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		upStatus, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 502, upStatus)
		require.Contains(t, item.str("error_message"), "Upstream node restarted")
	})

	t.Run("F8_Failover_RetriedSuccess_RecordsAttemptCounts", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(
			e2eUpstreamResponse{
				Status: http.StatusTooManyRequests,
				Body:   `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Account 1 throttled"}}`,
			},
			e2eUpstreamResponse{
				Status: http.StatusOK,
				Body:   `{"id":"resp_ok_retry","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"recovered"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`,
			},
		)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusOK, status)

		item := h.waitForSample(reqID)
		require.Equal(t, "success", item.str("result"))
		attempts, ok := item.intVal("attempt_count")
		require.True(t, ok && attempts == 2, "expected attempt_count=2, got %v", item["attempt_count"])
		failed, ok := item.intVal("failed_attempts")
		require.True(t, ok && failed == 1, "expected failed_attempts=1, got %v", item["failed_attempts"])

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_attempts_total", "outcome", "retried_success"))
		require.Equal(t, 1.0, snap.sum("sub2api_account_switches_total"))
	})

	t.Run("F8_SSEInBandFailure_CapturesUpstreamErrorCode", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		// Single account so failover exhausts or stream fails in-band
		h.accountRepo.mu.Lock()
		h.accountRepo.accounts = h.accountRepo.accounts[:1]
		h.accountRepo.mu.Unlock()
		sseFail := strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_sse_fail"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_sse_fail","status":"failed","error":{"code":"rate_limit_exceeded","message":"Stream quota exhausted"}}}`,
			"",
		}, "\n")
		h.upstream.setSequence(e2eUpstreamResponse{
			Status:      http.StatusOK,
			ContentType: "text/event-stream",
			Body:        sseFail,
		})
		_, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","stream":true,"input":"hi"}`)

		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_rate_limit", item.str("failure_reason"))
		require.Equal(t, "rate_limit_exceeded", item.str("upstream_error_code"))
		require.Contains(t, item.str("error_message"), "Stream quota exhausted")
	})

	// ---- F9: Realtime API failure_reason filter & search ----
	t.Run("F9_FailureReasonsList_SortedDistinct", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"1"}`)
		_, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"2"}`)
		_, _, _, id3 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"3"}`)
		_, _, _, id4 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyQuotaExhausted, `{"model":"gpt-4o","input":"4"}`)
		for _, id := range []string{id1, id2, id3, id4} {
			_ = h.waitForSample(id)
		}

		res := h.queryRealtime(nil)
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, []string{"client_forbidden", "client_not_found", "gateway_ratelimit"}, res.FailureReasons)
	})

	t.Run("F9_FilterByFailureReason_ExactMatch", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"1"}`)
		_, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"2"}`)
		_, _, _, id3 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"ok"}`)
		for _, id := range []string{id1, id2, id3} {
			_ = h.waitForSample(id)
		}

		q := url.Values{}
		q.Set("result", "failure")
		q.Set("failure_reason", "client_forbidden")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, id1, res.Items[0].str("request_id"))
		require.Equal(t, "client_forbidden", res.Items[0].str("failure_reason"))
		tot, _ := res.Summary.intVal("total")
		require.Equal(t, 1, tot)
	})

	t.Run("F9_FilterByResultSuccess_ExcludesFailures", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, idFail := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"1"}`)
		_, _, _, idOK := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"ok"}`)
		_ = h.waitForSample(idFail)
		_ = h.waitForSample(idOK)

		q := url.Values{}
		q.Set("result", "success")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, idOK, res.Items[0].str("request_id"))
	})

	t.Run("F9_SearchByUpstreamErrorCode", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusTooManyRequests,
			Body:   `{"error":{"type":"rate_limit_error","code":"unique_rl_code_991","message":"throttled"}}`,
		})
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"1"}`)
		h.upstream.setSequence(e2eUpstreamResponse{Status: http.StatusOK, Body: `{"id":"resp_ok","object":"response","model":"gpt-4o","status":"completed"}`})
		_, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"2"}`)
		_ = h.waitForSample(id1)
		_ = h.waitForSample(id2)

		q := url.Values{}
		q.Set("search", "unique_rl_code_991")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, id1, res.Items[0].str("request_id"))
	})

	t.Run("F9_SearchByErrorMessageAndHTTPMethod", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   `{"error":{"type":"invalid_request_error","code":"bad_param","message":"needle_msg_xyz_77"}}`,
		})
		_, _, _, idErr := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"1"}`)
		_, _, _, idGet := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "")
		_ = h.waitForSample(idErr)
		_ = h.waitForSample(idGet)

		q1 := url.Values{}
		q1.Set("search", "needle_msg_xyz_77")
		res1 := h.queryRealtime(q1)
		require.Len(t, res1.Items, 1)
		require.Equal(t, idErr, res1.Items[0].str("request_id"))

		q2 := url.Values{}
		q2.Set("search", "GET")
		res2 := h.queryRealtime(q2)
		foundGet := false
		for _, it := range res2.Items {
			if it.str("request_id") == idGet {
				foundGet = true
			}
		}
		require.True(t, foundGet, "search=GET should match GET request sample")
	})
}

// ---------------------------------------------------------------------------
// TIER 2: Boundary Value Analysis & Corner Cases (F1–F9, >=5 each = 45 subtests)
// ---------------------------------------------------------------------------

func TestE2E_OpsMonitoring_T2_BoundariesAndCorners(t *testing.T) {
	// ---- F1 BVA ----
	t.Run("F1_BVA_CaseInsensitiveUpgradeHeader", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, wsReqID := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":    "WebSocket",
			"Connection": "Upgrade",
		})
		require.Equal(t, http.StatusBadRequest, status) // coderws.Accept rejects missing Sec-WebSocket-Key
		h.sendSentinelAndAssertAbsent(wsReqID)
		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"))
	})

	t.Run("F1_BVA_MultiValueConnectionHeader", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, wsReqID := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":    "websocket",
			"Connection": "keep-alive, Upgrade",
		})
		require.Equal(t, http.StatusBadRequest, status)
		h.sendSentinelAndAssertAbsent(wsReqID)
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_ops_eligible_requests_total"))
	})

	t.Run("F1_BVA_WhitespaceUpgradeHeader", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, wsReqID := h.doHTTP(http.MethodGet, "/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":    " websocket ",
			"Connection": "Upgrade",
		})
		require.Equal(t, http.StatusBadRequest, status)
		h.sendSentinelAndAssertAbsent(wsReqID)
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_ops_eligible_requests_total"))
	})

	t.Run("F1_BVA_FakeUpgradeOnPostV1Responses_StillEligible", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`, map[string]string{
			"Upgrade":    "websocket",
			"Connection": "Upgrade",
		})
		require.Equal(t, http.StatusOK, status)
		_ = h.waitForSample(reqID)
		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_ops_eligible_requests_total"), "POST with fake Upgrade: websocket must NOT be excluded")
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total"))
	})

	t.Run("F1_BVA_GrokRealtime_NotInHTTPOpsRoute", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodGet, "/v1/realtime", e2eKeyGrok, "")
		require.Equal(t, http.StatusUpgradeRequired, status)
		h.sendSentinelAndAssertAbsent(reqID)
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_ops_eligible_requests_total"))
	})

	// ---- F2 BVA ----
	t.Run("F2_BVA_101WithNilBody_NoPartialBodyError", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), monitoring.NewOpsRuntime(inst.Ops))
		r.GET("/v1/images/batches", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/images/batches", nil))
		require.Equal(t, http.StatusSwitchingProtocols, w.Code)

		families, err := inst.Registry.Gather()
		require.NoError(t, err)
		for _, f := range families {
			if f.GetName() == "sub2api_gateway_requests_total" {
				for _, m := range f.GetMetric() {
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "result" {
							require.Equal(t, "success", lp.GetValue())
						}
						if lp.GetName() == "failure_reason" {
							require.Equal(t, "", lp.GetValue())
						}
					}
				}
			}
		}
	})

	t.Run("F2_BVA_101And200Mixed_BothSuccess", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), monitoring.NewOpsRuntime(inst.Ops))
		r.GET("/v1/images/batches", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })
		r.POST("/v1/images/batches", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/images/batches", nil))
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/images/batches", strings.NewReader("{}")))

		families, err := inst.Registry.Gather()
		require.NoError(t, err)
		var successSum float64
		for _, f := range families {
			if f.GetName() == "sub2api_gateway_requests_total" {
				for _, m := range f.GetMetric() {
					var res string
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "result" {
							res = lp.GetValue()
						}
					}
					require.Equal(t, "success", res)
					successSum += m.GetCounter().GetValue()
				}
			}
		}
		require.Equal(t, 2.0, successSum)
	})

	t.Run("F2_BVA_399Vs400Boundary", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), monitoring.NewOpsRuntime(inst.Ops))
		r.GET("/v1/images/batches", func(c *gin.Context) { c.Status(399) })
		r.POST("/v1/images/batches", func(c *gin.Context) { c.Status(400) })

		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/images/batches", nil))
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/images/batches", nil))

		families, err := inst.Registry.Gather()
		require.NoError(t, err)
		counts := map[string]float64{}
		for _, f := range families {
			if f.GetName() == "sub2api_gateway_requests_total" {
				for _, m := range f.GetMetric() {
					var res, reason string
					for _, lp := range m.GetLabel() {
						if lp.GetName() == "result" {
							res = lp.GetValue()
						}
						if lp.GetName() == "failure_reason" {
							reason = lp.GetValue()
						}
					}
					counts[res+":"+reason] += m.GetCounter().GetValue()
				}
			}
		}
		require.Equal(t, 1.0, counts["success:"])
		require.Equal(t, 1.0, counts["rejected:client_bad_request"])
	})

	t.Run("F2_BVA_WS101MultiTurnSession_NoHttpGatewayError", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, wsReqID, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		for i := 0; i < 3; i++ {
			wctx, wc := context.WithTimeout(context.Background(), 3*time.Second)
			require.NoError(t, conn.Write(wctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-4o","input":"turn"}`)))
			wc()
			rctx, rc := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, rerr := conn.Read(rctx)
			rc()
			require.NoError(t, rerr)
		}
		require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
		h.sendSentinelAndAssertAbsent(wsReqID)
		require.Zero(t, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})

	t.Run("F2_BVA_WS101AbnormalDrop_NoHttpGatewayError", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, wsReqID, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		_ = conn.CloseNow() // immediate TCP drop without close frame
		h.sendSentinelAndAssertAbsent(wsReqID)
		require.Zero(t, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})

	// ---- F3 BVA ----
	t.Run("F3_BVA_InitialWSActiveGaugeIsZero", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		snap := h.scrapeMetrics()
		vResp, okResp := snap.value("sub2api_ws_active_connections", "endpoint", "responses")
		vReal, okReal := snap.value("sub2api_ws_active_connections", "endpoint", "realtime")
		require.True(t, okResp)
		require.True(t, okReal)
		require.Zero(t, vResp)
		require.Zero(t, vReal)
	})

	t.Run("F3_BVA_ConcurrentActiveGaugeTracksExactCount", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		c1, _, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = c1.CloseNow() }()
		c2, _, _, err := h.dialWS("/backend-api/codex/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = c2.CloseNow() }()

		require.Eventually(t, func() bool {
			return h.scrapeMetrics().sum("sub2api_ws_active_connections", "endpoint", "responses") == 2
		}, 3*time.Second, 25*time.Millisecond)

		require.NoError(t, c1.Close(coderws.StatusNormalClosure, "1"))
		require.Eventually(t, func() bool {
			return h.scrapeMetrics().sum("sub2api_ws_active_connections", "endpoint", "responses") == 1
		}, 3*time.Second, 25*time.Millisecond)

		require.NoError(t, c2.Close(coderws.StatusNormalClosure, "2"))
		require.Eventually(t, func() bool {
			return h.scrapeMetrics().sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("F3_BVA_WSHandshakeFailed_InvalidVersion", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, _ := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":               "websocket",
			"Connection":            "Upgrade",
			"Sec-WebSocket-Version": "99",
		})
		require.GreaterOrEqual(t, status, 400)
		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "handshake_failed") == 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("F3_BVA_NonUpgrade426_DoesNotStickActiveGauge", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, _ := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "")
		require.Equal(t, http.StatusUpgradeRequired, status)
		snap := h.scrapeMetrics()
		require.Zero(t, snap.sum("sub2api_ws_active_connections", "endpoint", "responses"))
		require.Zero(t, snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "connected"))
	})

	t.Run("F3_BVA_WSAbruptDisconnect_ZeroesActiveGaugeAndCountsOutcome", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return h.scrapeMetrics().sum("sub2api_ws_active_connections", "endpoint", "responses") == 1
		}, 3*time.Second, 25*time.Millisecond)
		_ = conn.CloseNow()

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			closedErr := snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_error")
			closedNorm := snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal")
			return snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0 && (closedErr+closedNorm) == 1
		}, 3*time.Second, 25*time.Millisecond)
	})

	// ---- F4 BVA ----
	t.Run("F4_BVA_UserInactive401_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyInactiveUser, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusUnauthorized, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "inactive@e2e.test", "inactive-user-key", "openai-std-group")
	})

	t.Run("F4_BVA_DisabledKey401_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyDisabled, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusUnauthorized, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "disabled-key", "openai-std-group")
	})

	t.Run("F4_BVA_UngroupedKey403_RetainsUserAndKeyMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyUngrouped, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "ungrouped-key", "")
	})

	t.Run("F4_BVA_ZeroBalance403_RetainsUserKeyGroupMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyZeroBalance, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "zerobal@e2e.test", "zero-bal-key", "openai-std-group")
	})

	t.Run("F4_BVA_QueryAPIKey400_NoPanicAndRedacted", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses?key=sk-secretquerykey123", "", `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_bad_request", item.str("failure_reason"))
		require.NotContains(t, item.str("error_message"), "sk-secretquerykey123")
	})

	// ---- F5 BVA ----
	t.Run("F5_BVA_EmptyErrorFieldsOn200", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusOK, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "success", item.str("result"))
		require.Empty(t, item.str("error_message"))
		require.False(t, item.hasKey("upstream_status"))
		require.Empty(t, item.str("upstream_error_code"))
	})

	t.Run("F5_BVA_MultibyteUTF8Truncation_ValidUTF8", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		cjk := strings.Repeat("模型超限🔥", 120) // 600 runes, multi-byte
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"utf8_long","message":%q}}`, cjk),
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)

		item := h.waitForSample(reqID)
		msg := item.str("error_message")
		require.True(t, utf8.ValidString(msg), "truncated error_message must remain valid UTF-8")
		require.LessOrEqual(t, utf8.RuneCountInString(msg), 512)
		require.GreaterOrEqual(t, utf8.RuneCountInString(msg), 256)
	})

	t.Run("F5_BVA_QuerySecretsRedacted_KeyApiKeyToken", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   `{"error":{"type":"invalid_request_error","code":"url_leak","message":"failed calling https://u.test/a?key=secretA111&api_key=secretB222&token=secretC333"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)

		item := h.waitForSample(reqID)
		msg := item.str("error_message")
		require.NotEmpty(t, msg)
		require.NotContains(t, msg, "secretA111")
		require.NotContains(t, msg, "secretB222")
		require.NotContains(t, msg, "secretC333")
	})

	t.Run("F5_BVA_BoundaryLength512Vs513Runes", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		exact512 := strings.Repeat("x", 512)
		over513 := strings.Repeat("y", 513)

		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"exact512","message":%q}}`, exact512),
		})
		_, _, _, id512 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"1"}`)

		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"over513","message":%q}}`, over513),
		})
		_, _, _, id513 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"2"}`)

		it512 := h.waitForSample(id512)
		it513 := h.waitForSample(id513)
		require.Equal(t, 512, utf8.RuneCountInString(it512.str("error_message")))
		require.LessOrEqual(t, utf8.RuneCountInString(it513.str("error_message")), 512)
	})

	t.Run("F5_BVA_MultipleSecretFormatsInSingleError", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusUnauthorized,
			Body:   `{"error":{"type":"authentication_error","code":"multi_leak","message":"bad sk-ant-api03-leakalpha111 and Bearer tok_leakbeta222 and sk-proj-leakgamma333"}}`,
		})
		_, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		item := h.waitForSample(reqID)
		msg := item.str("error_message")
		require.NotEmpty(t, msg)
		require.NotContains(t, msg, "leakalpha111")
		require.NotContains(t, msg, "leakbeta222")
		require.NotContains(t, msg, "leakgamma333")
	})

	// ---- F6 BVA ----
	t.Run("F6_BVA_400_MalformedJSONBody", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_bad_request", item.str("failure_reason"))
	})

	t.Run("F6_BVA_401_NonBearerAuthHeader", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", "", `{"model":"gpt-4o","input":"hi"}`, map[string]string{
			"Authorization": "Basic dXNlcjpwYXNz",
		})
		require.Equal(t, http.StatusUnauthorized, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_auth_failed", item.str("failure_reason"))
	})

	t.Run("F6_BVA_403_ExpiredKey", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyExpired, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_forbidden", item.str("failure_reason"))
	})

	t.Run("F6_BVA_404_UnsupportedPlatformEndpoint", func(t *testing.T) {
		// Non-forwardable /v1/responses/*subpath probes are intentionally excluded from
		// isOpsGatewayRoute; test an ops-eligible gateway route that rejects with 404.
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/embeddings", e2eKeyAnthropic, `{"model":"text-embedding-3-small","input":"hi"}`)
		require.Equal(t, http.StatusNotFound, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "client_not_found", item.str("failure_reason"))
		require.Contains(t, item.str("error_message"), "Embeddings API is not supported")
	})

	t.Run("F6_BVA_499_ClientCanceledContextDuringAuth", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		reqID := uuid.NewString()
		h.apiKeyRepo.mu.Lock()
		h.apiKeyRepo.onGetByKeyAuth = func(rctx context.Context, _ string) (*service.APIKey, error) {
			cancel()
			select {
			case <-rctx.Done():
				return nil, rctx.Err()
			case <-time.After(3 * time.Second):
				return nil, context.DeadlineExceeded
			}
		}
		h.apiKeyRepo.mu.Unlock()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.appServer.URL+"/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer sk-cancel-lookup-key")
		req.Header.Set("X-Request-Id", reqID)
		_, doErr := h.httpClient.Do(req)
		require.Error(t, doErr)

		item := h.waitForSample(reqID)
		require.Equal(t, "client_abort", item.str("result"))
		reason := item.str("failure_reason")
		require.True(t, reason == "client_abort" || reason == "client_abort_during_body_read", "unexpected 499 failure_reason=%q", reason)
	})

	// ---- F7 BVA ----
	t.Run("F7_BVA_PanicWithErrorValue", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.onListSchedulable = func(context.Context, string) ([]service.Account, error) {
			panic(errors.New("err panic with sk-secretval555666"))
		}
		h.accountRepo.mu.Unlock()

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusInternalServerError, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "gateway_panic", item.str("failure_reason"))
		require.NotEmpty(t, item.str("error_message"))
		require.NotContains(t, item.str("error_message"), "sk-secretval555666")
	})

	t.Run("F7_BVA_PanicAfterPartialSSEWrite", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		sw := service.NewOpsSampleWriter(rdb, 50, 24)
		sw.Start()
		t.Cleanup(sw.Stop)

		rt := monitoring.NewOpsRuntime(inst.Ops)
		rt.SampleSink = sw
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), rt)
		r.Use(middleware.RequestLogger())
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.String(http.StatusOK, "data: partial\n\n")
			c.Writer.Flush()
			panic("stream panic Bearer sk-streamsecret999")
		})
		reqID := uuid.NewString()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
		req.Header.Set("X-Request-Id", reqID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		require.Eventually(t, func() bool {
			res, err := sw.ListRealtimeRequests(context.Background(), service.RealtimeRequestsFilter{Search: reqID, Limit: 10})
			if err != nil || len(res.Items) == 0 {
				return false
			}
			raw, _ := json.Marshal(res.Items[0])
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			require.Equal(t, "gateway_error", m["result"])
			require.Equal(t, "gateway_panic", m["failure_reason"])
			msg, _ := m["error_message"].(string)
			require.NotEmpty(t, msg)
			require.NotContains(t, msg, "sk-streamsecret999")
			return true
		}, 3*time.Second, 50*time.Millisecond)
	})

	t.Run("F7_BVA_BodyLimitExact4096Vs4097Bytes", func(t *testing.T) {
		h := newE2EOpsHarness(t) // MaxBodySize = 4096
		prefix := `{"model":"gpt-4o","input":"`
		suffix := `"}`
		exactPayload := prefix + strings.Repeat("a", 4096-len(prefix)-len(suffix)) + suffix
		require.Len(t, exactPayload, 4096)
		overPayload := prefix + strings.Repeat("a", 4097-len(prefix)-len(suffix)) + suffix
		require.Len(t, overPayload, 4097)

		stOK, _, _, idOK := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, exactPayload)
		require.Equal(t, http.StatusOK, stOK)
		_ = h.waitForSample(idOK)

		st413, _, _, id413 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, overPayload)
		require.Equal(t, http.StatusRequestEntityTooLarge, st413)
		it413 := h.waitForSample(id413)
		require.Equal(t, "body_too_large", it413.str("failure_reason"))
		require.NotEmpty(t, it413.str("error_message"))
	})

	t.Run("F7_BVA_ConflictingNestedField_CapturesFieldPath", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","reasoning":{"effort":"low","effort":"high"},"input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "client_bad_request", item.str("failure_reason"))
		require.Contains(t, item.str("error_message"), "effort")
	})

	t.Run("F7_BVA_GroupDeleted403_CapturesErrorMessage", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyDeletedGroup, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusForbidden, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "client_forbidden", item.str("failure_reason"))
		require.NotEmpty(t, item.str("error_message"))
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "deleted-grp-key", "deleted-group")
	})

	// ---- F8 BVA ----
	t.Run("F8_BVA_Upstream502_NonJSONBody", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status:      http.StatusBadGateway,
			ContentType: "text/plain",
			Body:        "502 Bad Gateway from edge proxy",
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_server_error", item.str("failure_reason"))
		upSt, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 502, upSt)
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("F8_BVA_Upstream404_ModelNotFound", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusNotFound,
			Body:   `{"error":{"type":"not_found_error","code":"model_not_found","message":"Model gpt-unknown not found"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_model_or_request", item.str("failure_reason"))
		upSt, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 404, upSt)
		require.Equal(t, "model_not_found", item.str("upstream_error_code"))
	})

	t.Run("F8_BVA_Upstream403_UpstreamAuth", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusForbidden,
			Body:   `{"error":{"type":"permission_error","code":"insufficient_permissions","message":"Upstream org forbidden"}}`,
		})
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)
		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_auth", item.str("failure_reason"))
		upSt, ok := item.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 403, upSt)
		require.Equal(t, "insufficient_permissions", item.str("upstream_error_code"))
	})

	t.Run("F8_BVA_NoAvailableAccounts503_CapturesErrorAndMeta", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.setAccounts() // zero accounts
		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusServiceUnavailable, status)
		item := h.waitForSample(reqID)
		require.NotEmpty(t, item.str("error_message"))
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "openai-main-key", "openai-std-group")
	})

	t.Run("F8_BVA_ConsecutiveRequests_DoNotLeakErrorDetails", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(
			e2eUpstreamResponse{
				Status: http.StatusTooManyRequests,
				Body:   `{"error":{"type":"rate_limit_error","code":"leak_check_code","message":"Failure A"}}`,
			},
			e2eUpstreamResponse{
				Status: http.StatusTooManyRequests,
				Body:   `{"error":{"type":"rate_limit_error","code":"leak_check_code","message":"Failure A"}}`,
			},
			e2eUpstreamResponse{
				Status: http.StatusOK,
				Body:   `{"id":"resp_clean","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg_clean","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`,
			},
		)
		st1, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"fail"}`)
		require.Equal(t, http.StatusTooManyRequests, st1)
		it1 := h.waitForSample(id1)
		require.Equal(t, "leak_check_code", it1.str("upstream_error_code"))

		h.openaiSvc.ClearAccountSchedulingBlock(3001)
		h.openaiSvc.ClearAccountSchedulingBlock(3002)
		st2, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"ok"}`)
		require.Equal(t, http.StatusOK, st2)
		it2 := h.waitForSample(id2)
		require.Equal(t, "success", it2.str("result"))
		require.Empty(t, it2.str("error_message"))
		require.False(t, it2.hasKey("upstream_status"))
		require.Empty(t, it2.str("upstream_error_code"))
	})

	// ---- F9 BVA ----
	t.Run("F9_BVA_FailureReasonAllAndEmptyEquivalent", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"1"}`)
		_, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"2"}`)
		_ = h.waitForSample(id1)
		_ = h.waitForSample(id2)

		qEmpty := url.Values{}
		qEmpty.Set("result", "failure")
		resEmpty := h.queryRealtime(qEmpty)

		qAll := url.Values{}
		qAll.Set("result", "failure")
		qAll.Set("failure_reason", "__all__")
		resAll := h.queryRealtime(qAll)

		require.Len(t, resEmpty.Items, 2)
		require.Len(t, resAll.Items, 2)
	})

	t.Run("F9_BVA_FailureReasonUnknown_ReturnsEmptyNonNilSlice", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"1"}`)
		_ = h.waitForSample(id1)

		q := url.Values{}
		q.Set("result", "failure")
		q.Set("failure_reason", "non_existent_reason_abc")
		res := h.queryRealtime(q)
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Empty(t, res.Items)
		tot, _ := res.Summary.intVal("total")
		require.Zero(t, tot)
	})

	t.Run("F9_BVA_LimitClampAndInvalid400", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		for i := 0; i < 3; i++ {
			_, _, _, id := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
			_ = h.waitForSample(id)
		}
		q1 := url.Values{}
		q1.Set("limit", "1")
		require.Len(t, h.queryRealtime(q1).Items, 1)

		q0 := url.Values{}
		q0.Set("limit", "0")
		require.Len(t, h.queryRealtime(q0).Items, 1)

		qBad := url.Values{}
		qBad.Set("limit", "notanumber")
		require.Equal(t, http.StatusBadRequest, h.queryRealtime(qBad).StatusCode)
	})

	t.Run("F9_BVA_ModelAndFailureReasonCombinedAND", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, id1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"1"}`)
		_, _, _, id2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-blocked","model":"gpt-4o","input":"2"}`)
		_ = h.waitForSample(id1)
		_ = h.waitForSample(id2)

		q := url.Values{}
		q.Set("result", "failure")
		q.Set("failure_reason", "client_not_found")
		q.Set("model", "gpt-blocked")
		res := h.queryRealtime(q)
		for _, it := range res.Items {
			require.Equal(t, "client_not_found", it.str("failure_reason"))
		}
		// Ensure id2 (client_bad_request) is excluded
		for _, it := range res.Items {
			require.NotEqual(t, id2, it.str("request_id"))
		}
	})

	t.Run("F9_BVA_CaseInsensitiveSearchAcrossErrorFields", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusTooManyRequests,
			Body:   `{"error":{"type":"rate_limit_error","code":"MixedCaseCode_XYZ","message":"Downstream throttling"}}`,
		})
		_, _, _, id := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		_ = h.waitForSample(id)

		q := url.Values{}
		q.Set("search", "mixedcasecode_xyz")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, id, res.Items[0].str("request_id"))
	})
}

// ---------------------------------------------------------------------------
// TIER 3: Pairwise Multi-Feature Combinations (>=10 subtests = 12 subtests)
// ---------------------------------------------------------------------------

func TestE2E_OpsMonitoring_T3_PairwiseCombinations(t *testing.T) {
	t.Run("P01_F1_F2_F3_WSLifecycleConcurrentWithHTTP200", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		conn, _, wsReqID, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		st, _, _, httpID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"concurrent"}`)
		require.Equal(t, http.StatusOK, st)
		_ = h.waitForSample(httpID)

		require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
		h.sendSentinelAndAssertAbsent(wsReqID)

		snap := h.scrapeMetrics()
		require.Equal(t, 2.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 2.0, snap.sum("sub2api_gateway_requests_total", "result", "success", "failure_reason", ""))
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
		require.Eventually(t, func() bool {
			s := h.scrapeMetrics()
			return s.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0 &&
				s.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal") == 1
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("P02_F4_F6_F9_AuthPassAllowlistFailAndFilter", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-disallowed","input":"hi"}`)
		require.Equal(t, http.StatusNotFound, st)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "allowlist-key", "allowlist-group")
		require.Equal(t, "client_not_found", item.str("failure_reason"))

		q := url.Values{}
		q.Set("result", "failure")
		q.Set("failure_reason", "client_not_found")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, reqID, res.Items[0].str("request_id"))
		require.Contains(t, res.FailureReasons, "client_not_found")
	})

	t.Run("P03_F5_F8_F9_Upstream429AllFailAndSearch", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.upstream.setSequence(
			e2eUpstreamResponse{Status: 429, Body: `{"error":{"type":"rate_limit_error","code":"rl_pair_03","message":"quota 1"}}`},
			e2eUpstreamResponse{Status: 429, Body: `{"error":{"type":"rate_limit_error","code":"rl_pair_03","message":"quota 2"}}`},
		)
		st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, st)
		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_rate_limit", item.str("failure_reason"))
		require.Equal(t, "rl_pair_03", item.str("upstream_error_code"))
		upSt, _ := item.intVal("upstream_status")
		require.Equal(t, 429, upSt)

		q := url.Values{}
		q.Set("failure_reason", "upstream_rate_limit")
		q.Set("search", "rl_pair_03")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, reqID, res.Items[0].str("request_id"))
	})

	t.Run("P04_F5_F6_F7_PanicWithSecretRedactionAndMetric", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.onListSchedulable = func(context.Context, string) ([]service.Account, error) {
			panic("fatal with https://x.io?token=panic_tok_999 and sk-ant-api03-panicsecret999")
		}
		h.accountRepo.mu.Unlock()

		st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusInternalServerError, st)
		item := h.waitForSample(reqID)
		require.Equal(t, "gateway_error", item.str("result"))
		require.Equal(t, "gateway_panic", item.str("failure_reason"))
		require.NotContains(t, item.str("error_message"), "panic_tok_999")
		require.NotContains(t, item.str("error_message"), "panicsecret999")
		assertSampleAuthMetaPopulated(t, item, "bob@e2e.test", "anthropic-main-key", "anthropic-std-group")
		require.Equal(t, 1.0, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "gateway_error", "failure_reason", "gateway_panic"))
	})

	t.Run("P05_F4_F6_F7_BodyLimit413WithAuthMetaAndErrorMsg", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		big := fmt.Sprintf(`{"model":"gpt-4o","input":%q}`, strings.Repeat("Z", 5000))
		st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, big)
		require.Equal(t, http.StatusRequestEntityTooLarge, st)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "openai-main-key", "openai-std-group")
		require.Equal(t, "rejected", item.str("result"))
		require.Equal(t, "body_too_large", item.str("failure_reason"))
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("P06_F4_F6_F9_Concurrency429WithMetaAndFilter", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.concurrencyCache.mu.Lock()
		h.concurrencyCache.rejectUserSlot = true
		h.concurrencyCache.mu.Unlock()

		st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, st)
		item := h.waitForSample(reqID)
		assertSampleAuthMetaPopulated(t, item, "alice@e2e.test", "openai-main-key", "openai-std-group")
		require.Equal(t, "gateway_ratelimit", item.str("failure_reason"))

		q := url.Values{}
		q.Set("result", "failure")
		q.Set("failure_reason", "gateway_ratelimit")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
	})

	t.Run("P07_F1_F2_F5_NonUpgrade426VsUpgrade101OnSamePath", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		st426, _, _, id426 := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "")
		require.Equal(t, http.StatusUpgradeRequired, st426)
		wsID := h.runHappyWSRoundTrip("/v1/responses", e2eKeyOpenAI)
		h.sendSentinelAndAssertAbsent(wsID)

		it426 := h.waitForSample(id426)
		require.Equal(t, "GET", it426.str("http_method"))
		require.Equal(t, "rejected", it426.str("result"))
		snap := h.scrapeMetrics()
		require.Equal(t, 2.0, snap.sum("sub2api_ops_eligible_requests_total")) // 426 + sentinel
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})

	t.Run("P08_F3_F4_F6_WSAuth401AndCapacity429Rejection", func(t *testing.T) {
		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.maxWSIngressPerKey = 1
		})
		// WS 401 unauthorized
		_, resp401, ws401ID, err := h.dialWS("/v1/responses", "sk-invalid-ws-key")
		require.Error(t, err)
		require.NotNil(t, resp401)
		require.Equal(t, http.StatusUnauthorized, resp401.StatusCode)

		// WS open + 2nd capacity rejected 429
		c1, _, _, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		defer func() { _ = c1.CloseNow() }()
		_, resp429, ws429ID, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.Error(t, err)
		require.NotNil(t, resp429)
		require.Equal(t, http.StatusTooManyRequests, resp429.StatusCode)

		require.NoError(t, c1.Close(coderws.StatusNormalClosure, "done"))
		h.sendSentinelAndAssertAbsent(ws401ID)
		h.sendSentinelAndAssertAbsent(ws429ID)

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "rejected") >= 1 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
		require.Zero(t, h.scrapeMetrics().sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})

	t.Run("P09_F5_F8_F9_SSEInBandFailureWithRedactionAndSearch", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.accounts = h.accountRepo.accounts[:1]
		h.accountRepo.mu.Unlock()
		sseFail := strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_sse_sec"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_sse_sec","status":"failed","error":{"code":"invalid_request_error","message":"p09_marker_77 rejected sk-proj-sseleaksecret999"}}}`,
			"",
		}, "\n")
		h.upstream.setSequence(e2eUpstreamResponse{
			Status:      http.StatusOK,
			ContentType: "text/event-stream",
			Body:        sseFail,
		})
		_, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","stream":true,"input":"hi"}`)
		item := h.waitForSample(reqID)
		require.Equal(t, "upstream_model_or_request", item.str("failure_reason"))
		require.NotContains(t, item.str("error_message"), "sseleaksecret999")

		q := url.Values{}
		q.Set("search", "p09_marker_77")
		res := h.queryRealtime(q)
		require.Len(t, res.Items, 1)
		require.Equal(t, reqID, res.Items[0].str("request_id"))
	})

	t.Run("P10_F4_F7_F8_AnthropicMessagesConflictVsUpstream500", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		st400, _, _, id400 := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","model":"claude-3-opus","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadRequest, st400)

		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusInternalServerError,
			Body:   `{"type":"error","error":{"type":"api_error","message":"Anthropic backend down"}}`,
		})
		st500, _, _, id500 := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadGateway, st500)

		it400 := h.waitForSample(id400)
		it500 := h.waitForSample(id500)
		require.Equal(t, "client_bad_request", it400.str("failure_reason"))
		require.Equal(t, "upstream_server_error", it500.str("failure_reason"))
		assertSampleAuthMetaPopulated(t, it400, "bob@e2e.test", "anthropic-main-key", "anthropic-std-group")
		assertSampleAuthMetaPopulated(t, it500, "bob@e2e.test", "anthropic-main-key", "anthropic-std-group")
	})

	t.Run("P11_F2_F5_F9_Eligible101And4xxSummaryParity", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, idOK := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"ok"}`)
		_, _, _, id403 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"403"}`)
		_, _, _, id404 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-blocked","input":"404"}`)
		_ = h.waitForSample(idOK)
		_ = h.waitForSample(id403)
		_ = h.waitForSample(id404)

		res := h.queryRealtime(nil)
		tot, _ := res.Summary.intVal("total")
		succ, _ := res.Summary.intVal("success")
		fail, _ := res.Summary.intVal("failure")
		rate, _ := res.Summary.floatVal("success_rate")
		require.Equal(t, 3, tot)
		require.Equal(t, 1, succ)
		require.Equal(t, 2, fail)
		require.InDelta(t, 33.3, rate, 0.2)
		require.Equal(t, []string{"client_forbidden", "client_not_found"}, res.FailureReasons)
	})

	t.Run("P12_F6_F7_F8_PreEarlyReturnOpsNilAcrossMiddlewareAndHandler", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		_, _, _, idMW := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyDisabledGroup, `{"model":"gpt-4o","input":"mw"}`)
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadGateway,
			Body:   `{"error":{"type":"server_error","code":"gw_502","message":"p12 upstream error"}}`,
		})
		_, _, _, idUp := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"up"}`)

		itMW := h.waitForSample(idMW)
		itUp := h.waitForSample(idUp)
		require.Equal(t, "client_forbidden", itMW.str("failure_reason"))
		require.NotEmpty(t, itMW.str("error_message"))
		require.Equal(t, "upstream_server_error", itUp.str("failure_reason"))
		upSt, ok := itUp.intVal("upstream_status")
		require.True(t, ok)
		require.Equal(t, 502, upSt)
		require.Contains(t, itUp.str("error_message"), "p12 upstream error")
	})
}

// ---------------------------------------------------------------------------
// TIER 4: Real-World End-to-End Application Scenarios (S1–S4)
// ---------------------------------------------------------------------------

func TestE2E_OpsMonitoring_T4_RealWorldScenarios(t *testing.T) {
	t.Run("S1_MixedAIGatewayTrafficBurst", func(t *testing.T) {
		h := newE2EOpsHarness(t)

		// 1. Two WS Codex sessions (/v1/responses & /backend-api/codex/responses)
		ws1 := h.runHappyWSRoundTrip("/v1/responses", e2eKeyOpenAI)
		ws2 := h.runHappyWSRoundTrip("/backend-api/codex/responses", e2eKeyOpenAI)

		// 2. HTTP SSE streaming 200 on /v1/responses + JSON 200 on /v1/messages
		h.upstream.setHTTPHandler(func(_ int, r *http.Request, _ []byte) e2eUpstreamResponse {
			if strings.Contains(r.URL.Path, "messages") {
				return e2eUpstreamResponse{
					Status:      http.StatusOK,
					ContentType: "application/json",
					Body:        `{"id":"msg_s1","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`,
				}
			}
			return e2eUpstreamResponse{
				Status:      http.StatusOK,
				ContentType: "text/event-stream",
				Body:        "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s1\",\"object\":\"response\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n",
			}
		})
		stOAI, _, _, idOAI := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","stream":true,"input":"burst"}`)
		require.Equal(t, http.StatusOK, stOAI)
		stAnt, _, _, idAnt := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"burst"}]}`)
		require.Equal(t, http.StatusOK, stAnt)

		// 3. Model allowlist 404 rejection
		st404, _, _, id404 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyModelAllowlist, `{"model":"gpt-forbidden","input":"burst"}`)
		require.Equal(t, http.StatusNotFound, st404)

		// 4. Upstream 500 failure
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusInternalServerError,
			Body:   `{"error":{"type":"server_error","code":"s1_backend_err","message":"Upstream node panic"}}`,
		})
		st500, _, _, id500 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"fail"}`)
		require.Equal(t, http.StatusBadGateway, st500)

		for _, id := range []string{idOAI, idAnt, id404, id500} {
			_ = h.waitForSample(id)
		}

		// Verify WS exclusion & isolation
		resAll := h.queryRealtime(nil)
		require.Equal(t, 4, len(resAll.Items), "expected exactly 4 HTTP samples (no WS samples)")
		for _, it := range resAll.Items {
			require.NotEqual(t, ws1, it.str("request_id"))
			require.NotEqual(t, ws2, it.str("request_id"))
			require.Equal(t, "POST", it.str("http_method"))
		}
		require.Equal(t, []string{"client_not_found", "upstream_server_error"}, resAll.FailureReasons)

		snap := h.scrapeMetrics()
		require.Equal(t, 4.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 4.0, snap.sum("sub2api_gateway_requests_total"))
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
		require.Eventually(t, func() bool {
			s := h.scrapeMetrics()
			return s.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "connected") == 2 &&
				s.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal") == 2 &&
				s.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)
	})

	t.Run("S2_CascadingRateLimitAndQuotaIncidentTriage", func(t *testing.T) {
		h := newE2EOpsHarness(t)

		// 1. Quota-exhausted API key -> 429 gateway_ratelimit
		stQuota, _, _, idQuota := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyQuotaExhausted, `{"model":"gpt-4o","input":"q1"}`)
		require.Equal(t, http.StatusTooManyRequests, stQuota)

		// 2. User concurrency exhausted -> 429 gateway_ratelimit
		h.concurrencyCache.mu.Lock()
		h.concurrencyCache.rejectUserSlot = true
		h.concurrencyCache.mu.Unlock()
		stConc, _, _, idConc := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"q2"}`)
		require.Equal(t, http.StatusTooManyRequests, stConc)
		h.concurrencyCache.mu.Lock()
		h.concurrencyCache.rejectUserSlot = false
		h.concurrencyCache.mu.Unlock()

		// 3. Upstream 429 rate limit -> upstream_rate_limit
		h.upstream.setSequence(
			e2eUpstreamResponse{Status: 429, Body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"RPM cap hit"}}`},
			e2eUpstreamResponse{Status: 429, Body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"RPM cap hit"}}`},
		)
		stUp429, _, _, idUp := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"q3"}`)
		require.Equal(t, http.StatusTooManyRequests, stUp429)

		for _, id := range []string{idQuota, idConc, idUp} {
			_ = h.waitForSample(id)
		}

		// Operator triage via realtime-requests filters
		qFail := url.Values{}
		qFail.Set("result", "failure")
		resFail := h.queryRealtime(qFail)
		require.Len(t, resFail.Items, 3)
		require.Equal(t, []string{"gateway_ratelimit", "upstream_rate_limit"}, resFail.FailureReasons)

		qGW := url.Values{}
		qGW.Set("result", "failure")
		qGW.Set("failure_reason", "gateway_ratelimit")
		resGW := h.queryRealtime(qGW)
		require.Len(t, resGW.Items, 2)
		for _, it := range resGW.Items {
			assertSampleAuthMetaPopulated(t, it, "alice@e2e.test", it.str("api_key_name"), "openai-std-group")
		}

		qUp := url.Values{}
		qUp.Set("result", "failure")
		qUp.Set("failure_reason", "upstream_rate_limit")
		qUp.Set("search", "rate_limit_exceeded")
		resUp := h.queryRealtime(qUp)
		require.Len(t, resUp.Items, 1)
		require.Equal(t, idUp, resUp.Items[0].str("request_id"))
		upSt, _ := resUp.Items[0].intVal("upstream_status")
		require.Equal(t, 429, upSt)

		snap := h.scrapeMetrics()
		require.Equal(t, 2.0, snap.sum("sub2api_gateway_requests_total", "result", "rejected", "failure_reason", "gateway_ratelimit"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total", "result", "upstream_error", "failure_reason", "upstream_rate_limit"))
	})

	t.Run("S3_SecurityAndSecretRedactionAudit", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		secrets := []string{
			"sk-ant-api03-auditsecret0001",
			"eyJhbGciOiJIUzI1NiJ9.auditjwtpayload0002",
			"auditquerysecret0003",
			"sk-auditpanicsecret0004",
		}

		// 1. Upstream 400 leaking sk-ant-, Bearer JWT, and ?api_key=
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadRequest,
			Body:   fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":"sec_audit","message":"rejected %s and Bearer %s at https://api.test/r?api_key=%s"}}`, secrets[0], secrets[1], secrets[2]),
		})
		_, _, _, idUp := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"sec1"}`)

		// 2. Unauthorized 401
		_, _, _, id401 := h.doHTTP(http.MethodPost, "/v1/responses", "sk-bogus-audit-key", `{"model":"gpt-4o","input":"sec2"}`)

		// 3. IP-restricted 403
		_, _, _, id403 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyIPRestricted, `{"model":"gpt-4o","input":"sec3"}`)

		// 4. Panic carrying secret on /v1/messages
		h.accountRepo.mu.Lock()
		h.accountRepo.onListSchedulable = func(context.Context, string) ([]service.Account, error) {
			panic("audit panic with Bearer " + secrets[3])
		}
		h.accountRepo.mu.Unlock()
		_, _, _, idPanic := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","max_tokens":16,"messages":[{"role":"user","content":"sec4"}]}`)

		for _, id := range []string{idUp, id401, id403, idPanic} {
			_ = h.waitForSample(id)
		}

		// Audit ALL samples in realtime-requests: zero secret substrings, valid metadata on post-auth failures
		res := h.queryRealtime(nil)
		require.Len(t, res.Items, 4)
		for _, it := range res.Items {
			msg := it.str("error_message")
			require.NotEmpty(t, msg, "failure sample %s should have error_message", it.str("request_id"))
			for _, sec := range secrets {
				require.NotContains(t, msg, sec, "secret %s leaked in realtime error_message", sec)
			}
			if it.str("request_id") == id403 {
				assertSampleAuthMetaPopulated(t, it, "alice@e2e.test", "ip-restricted-key", "openai-std-group")
				require.Equal(t, "client_forbidden", it.str("failure_reason"))
			}
			if it.str("request_id") == idPanic {
				assertSampleAuthMetaPopulated(t, it, "bob@e2e.test", "anthropic-main-key", "anthropic-std-group")
				require.Equal(t, "gateway_panic", it.str("failure_reason"))
			}
		}
	})

	t.Run("S4_WebSocketReconnectStormAndHTTPHealthIsolation", func(t *testing.T) {
		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.maxWSIngressPerKey = 2
		})

		// Open 2 concurrent WS connections -> 3rd gets rejected (429) -> malformed handshake -> close both -> reconnect 2 more
		c1, _, wsID1, err := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.NoError(t, err)
		c2, _, wsID2, err := h.dialWS("/backend-api/codex/responses", e2eKeyOpenAI)
		require.NoError(t, err)

		_, respRej, wsIDRej, errRej := h.dialWS("/v1/responses", e2eKeyOpenAI)
		require.Error(t, errRej)
		require.NotNil(t, respRej)
		require.Equal(t, http.StatusTooManyRequests, respRej.StatusCode)

		// Interleave 4 healthy HTTP POST /v1/responses requests during the WS storm
		httpIDs := make([]string, 0, 4)
		for i := 0; i < 4; i++ {
			st, _, _, id := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, fmt.Sprintf(`{"model":"gpt-4o","input":"http-%d"}`, i))
			require.Equal(t, http.StatusOK, st)
			httpIDs = append(httpIDs, id)
		}

		require.NoError(t, c1.Close(coderws.StatusNormalClosure, "storm-1"))
		require.NoError(t, c2.Close(coderws.StatusNormalClosure, "storm-2"))

		require.Eventually(t, func() bool {
			h.concurrencyCache.mu.Lock()
			leaseCount := 0
			for _, leases := range h.concurrencyCache.wsLeases {
				leaseCount += len(leases)
			}
			h.concurrencyCache.mu.Unlock()
			return leaseCount == 0 &&
				h.scrapeMetrics().sum("sub2api_ws_active_connections", "endpoint", "responses") == 0
		}, 3*time.Second, 25*time.Millisecond)

		stHS, _, _, wsIDHS := h.doHTTP(http.MethodGet, "/v1/responses", e2eKeyOpenAI, "", map[string]string{
			"Upgrade":    "websocket",
			"Connection": "Upgrade",
		})
		require.Equal(t, http.StatusBadRequest, stHS)

		wsID3 := h.runHappyWSRoundTrip("/v1/responses", e2eKeyOpenAI)
		wsID4 := h.runHappyWSRoundTrip("/responses", e2eKeyOpenAI)

		for _, id := range httpIDs {
			_ = h.waitForSample(id)
		}
		res := h.queryRealtime(nil)
		require.Len(t, res.Items, 4, "only the 4 HTTP requests should be in realtime-requests")
		for _, wsID := range []string{wsID1, wsID2, wsIDRej, wsIDHS, wsID3, wsID4} {
			for _, it := range res.Items {
				require.NotEqual(t, wsID, it.str("request_id"))
			}
		}
		rate, _ := res.Summary.floatVal("success_rate")
		require.Equal(t, 100.0, rate, "HTTP SLA must remain 100%% despite WS storm rejections/failures")

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_active_connections", "endpoint", "responses") == 0 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "connected") == 4 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "closed_normal") == 4 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "rejected") == 1 &&
				snap.sum("sub2api_ws_connections_total", "endpoint", "responses", "outcome", "handshake_failed") == 1
		}, 3*time.Second, 25*time.Millisecond)

		snap := h.scrapeMetrics()
		require.Equal(t, 4.0, snap.sum("sub2api_ops_eligible_requests_total"))
		require.Equal(t, 4.0, snap.sum("sub2api_gateway_requests_total", "result", "success", "failure_reason", ""))
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error"))
	})
}

func TestE2E_OpsMonitoring_P1P2_Regressions(t *testing.T) {
	t.Run("P1_1_ErrorCodeAndPlainTextSecretRedaction", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.accounts = h.accountRepo.accounts[:1]
		h.accountRepo.mu.Unlock()

		h.upstream.setSequence(
			e2eUpstreamResponse{
				Status: http.StatusInternalServerError,
				Body:   `{"error":{"type":"server_error","code":"refresh_token=rt_leak_secret_12345","message":"upstream failed client_secret=cs_leak_98765 and token: expired"}}`,
			},
			e2eUpstreamResponse{
				Status: http.StatusInternalServerError,
				Body:   `{"error":{"code":{"secret":"nested_obj"},"message":"bad object code"}}`,
			},
		)

		status1, _, _, reqID1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status1)
		item1 := h.waitForSample(reqID1)
		require.NotContains(t, item1.str("upstream_error_code"), "rt_leak_secret_12345")
		require.Contains(t, item1.str("upstream_error_code"), "refresh_token=***")
		require.NotContains(t, item1.str("error_message"), "cs_leak_98765")
		require.Contains(t, item1.str("error_message"), "client_secret=***")
		require.Contains(t, item1.str("error_message"), "token: expired")

		status2, _, _, reqID2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status2)
		item2 := h.waitForSample(reqID2)
		require.Empty(t, item2.str("upstream_error_code"))
	})

	t.Run("P1_2_CommittedHTTP200ZeroAttemptStreamFailure", func(t *testing.T) {
		cfg := &config.Config{Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true}}
		inst, err := monitoring.ProvideConfiguredRegistryAndMetrics(cfg)
		require.NoError(t, err)
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		sw := service.NewOpsSampleWriter(rdb, 50, 24)
		sw.Start()
		t.Cleanup(sw.Stop)

		rt := monitoring.NewOpsRuntime(inst.Ops)
		rt.SampleSink = sw
		r := gin.New()
		installObservability(r, cfg, middleware.ProvideMetricsMiddleware(cfg, inst.Metrics), rt)
		r.Use(middleware.RequestLogger())
		r.POST("/v1/responses", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.String(http.StatusOK, ": ping\n\n")
			c.Writer.Flush()
			service.MarkOpsStreamFailure(c, "billing_error", "insufficient_balance", "Insufficient quota", http.StatusTooManyRequests)
			c.String(http.StatusOK, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"insufficient_balance\",\"message\":\"Insufficient quota\"}}}\n\n")
		})

		reqID := uuid.NewString()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true}`))
		req.Header.Set("X-Request-Id", reqID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var found e2eRealtimeItem
		require.Eventually(t, func() bool {
			res, err := sw.ListRealtimeRequests(context.Background(), service.RealtimeRequestsFilter{Search: reqID, Limit: 10})
			if err != nil || len(res.Items) == 0 {
				return false
			}
			raw, _ := json.Marshal(res.Items[0])
			_ = json.Unmarshal(raw, &found)
			return true
		}, 3*time.Second, 25*time.Millisecond)

		httpStatus, ok := found.intVal("http_status")
		require.True(t, ok && httpStatus == http.StatusOK, "expected http_status=200, got %v", found["http_status"])
		require.Equal(t, "rejected", found.str("result"))
		require.Equal(t, "gateway_ratelimit", found.str("failure_reason"))
		attempts, hasAttempts := found.intVal("attempt_count")
		require.True(t, !hasAttempts || attempts == 0, "expected 0 attempts, got %v", found["attempt_count"])

		fams, err := inst.Registry.Gather()
		require.NoError(t, err)
		var rejCount, okCount float64
		for _, fam := range fams {
			if fam.GetName() != "sub2api_gateway_requests_total" {
				continue
			}
			for _, m := range fam.GetMetric() {
				var resLbl, reasonLbl string
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "result" {
						resLbl = lp.GetValue()
					} else if lp.GetName() == "failure_reason" {
						reasonLbl = lp.GetValue()
					}
				}
				if resLbl == "rejected" && reasonLbl == "gateway_ratelimit" {
					rejCount += m.GetCounter().GetValue()
				}
				if resLbl == "success" {
					okCount += m.GetCounter().GetValue()
				}
			}
		}
		require.EqualValues(t, 1, rejCount)
		require.Zero(t, okCount)
	})

	t.Run("P1_3_AnthropicHTTP200SSEOverloadedErrorWhenLogBodyDisabled", func(t *testing.T) {
		filterAnthropicOnly := func(h *e2eOpsHarness) {
			h.accountRepo.mu.Lock()
			defer h.accountRepo.mu.Unlock()
			var antAccounts []service.Account
			for _, acc := range h.accountRepo.accounts {
				if acc.Platform == service.PlatformAnthropic {
					antAccounts = append(antAccounts, acc)
				}
			}
			h.accountRepo.accounts = antAccounts
		}

		t.Run("BeforeOutput", func(t *testing.T) {
			h := newE2EOpsHarness(t)
			require.False(t, h.cfg.Gateway.LogUpstreamErrorBody)
			filterAnthropicOnly(h)

			sseImmediate := strings.Join([]string{
				"event: error",
				`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
				"",
				"",
			}, "\n")
			h.upstream.setSequence(e2eUpstreamResponse{
				Status:      http.StatusOK,
				ContentType: "text/event-stream",
				Body:        sseImmediate,
			})

			_, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
			item := h.waitForSample(reqID)
			require.Equal(t, "upstream_error", item.str("result"))
			require.Equal(t, "upstream_rate_limit", item.str("failure_reason"))
			require.Equal(t, "overloaded_error", item.str("upstream_error_code"))
			require.Contains(t, item.str("error_message"), "Overloaded")
		})

		t.Run("AfterMessageStart", func(t *testing.T) {
			h := newE2EOpsHarness(t)
			require.False(t, h.cfg.Gateway.LogUpstreamErrorBody)
			filterAnthropicOnly(h)

			sseMidStream := strings.Join([]string{
				"event: message_start",
				`data: {"type":"message_start","message":{"id":"msg_p1_3","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
				"",
				"event: error",
				`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded mid-stream"}}`,
				"",
				"",
			}, "\n")
			h.upstream.setSequence(e2eUpstreamResponse{
				Status:      http.StatusOK,
				ContentType: "text/event-stream",
				Body:        sseMidStream,
			})

			st, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, http.StatusOK, st)
			item := h.waitForSample(reqID)
			stVal, ok := item.intVal("http_status")
			require.True(t, ok && stVal == http.StatusOK)
			require.Equal(t, "upstream_error", item.str("result"))
			require.Equal(t, "upstream_rate_limit", item.str("failure_reason"))
			require.Equal(t, "overloaded_error", item.str("upstream_error_code"))
			require.Contains(t, item.str("error_message"), "Overloaded")
		})
	})

	t.Run("P1_4_OpenAIResponsesAndMessagesPanicRecovery", func(t *testing.T) {
		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.customKeyMutator = func(keyRepo *e2eAPIKeyRepo, _ *e2eAccountRepo, _ *e2eMockUpstream) {
				keyRepo.mu.Lock()
				defer keyRepo.mu.Unlock()
				if k := keyRepo.keys[e2eKeyOpenAI]; k != nil && k.Group != nil {
					k.Group.AllowMessagesDispatch = true
				}
			}
		})
		h.accountRepo.mu.Lock()
		h.accountRepo.onListSchedulable = func(context.Context, string) ([]service.Account, error) {
			panic("boom Bearer sk-e2epanicsecret999")
		}
		h.accountRepo.mu.Unlock()

		stResp, _, _, reqIDResp := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, stResp)
		itemResp := h.waitForSample(reqIDResp)
		require.Equal(t, "gateway_error", itemResp.str("result"))
		require.Equal(t, "gateway_panic", itemResp.str("failure_reason"))
		require.NotEmpty(t, itemResp.str("error_message"))
		require.NotContains(t, itemResp.str("error_message"), "sk-e2epanicsecret999")

		stMsg, _, _, reqIDMsg := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyOpenAI, `{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusInternalServerError, stMsg)
		itemMsg := h.waitForSample(reqIDMsg)
		require.Equal(t, "gateway_error", itemMsg.str("result"))
		require.Equal(t, "gateway_panic", itemMsg.str("failure_reason"))
		require.NotEmpty(t, itemMsg.str("error_message"))
		require.NotContains(t, itemMsg.str("error_message"), "sk-e2epanicsecret999")

		snap := h.scrapeMetrics()
		require.Equal(t, 2.0, snap.sum("sub2api_gateway_requests_total", "result", "gateway_error", "failure_reason", "gateway_panic"))
	})

	t.Run("P2_1_Attempt2TransportErrorDoesNotInheritAttempt1Status", func(t *testing.T) {
		hijackCloseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}))
		t.Cleanup(hijackCloseSrv.Close)

		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.customKeyMutator = func(_ *e2eAPIKeyRepo, accRepo *e2eAccountRepo, _ *e2eMockUpstream) {
				accRepo.mu.Lock()
				defer accRepo.mu.Unlock()
				for i := range accRepo.accounts {
					if accRepo.accounts[i].ID == 3002 {
						accRepo.accounts[i].Credentials["base_url"] = hijackCloseSrv.URL
					}
				}
			}
		})
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadGateway,
			Body:   `{"error":{"type":"server_error","code":"err_attempt_1","message":"upstream 1 failed"}}`,
		})

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		attempts, ok := item.intVal("attempt_count")
		require.True(t, ok && attempts == 2, "expected attempt_count=2, got %v", item["attempt_count"])
		upStatus, hasUpStatus := item.intVal("upstream_status")
		require.True(t, !hasUpStatus || upStatus == 0, "expected upstream_status cleared on attempt 2 transport error, got %v", item["upstream_status"])
		require.Empty(t, item.str("upstream_error_code"))
		require.NotEmpty(t, item.str("error_message"))
	})

	t.Run("P2_3_LiveSidebandWebSocketRoutesExcludedFromHTTPAndTrackLiveMetrics", func(t *testing.T) {
		h := newE2EOpsHarness(t)

		_, resp1, reqID1, err1 := h.dialWS("/v1/live/call_missing", e2eKeyOpenAI)
		require.Error(t, err1)
		require.NotNil(t, resp1)
		require.Equal(t, http.StatusForbidden, resp1.StatusCode)

		_, resp2, reqID2, err2 := h.dialWS("/backend-api/codex/call_missing", e2eKeyOpenAI)
		require.Error(t, err2)
		require.NotNil(t, resp2)
		require.Equal(t, http.StatusForbidden, resp2.StatusCode)

		require.Eventually(t, func() bool {
			snap := h.scrapeMetrics()
			return snap.sum("sub2api_ws_connections_total", "endpoint", "live", "outcome", "rejected") == 2 &&
				snap.sum("sub2api_ws_active_connections", "endpoint", "live") == 0
		}, 3*time.Second, 25*time.Millisecond)

		snapBeforeSentinel := h.scrapeMetrics()
		require.Zero(t, snapBeforeSentinel.sum("sub2api_ops_eligible_requests_total"))

		stSent, _, _, sentinelID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"sentinel-live"}`)
		require.Equal(t, http.StatusOK, stSent)
		_ = h.waitForSample(sentinelID)
		res := h.queryRealtime(nil)
		for _, it := range res.Items {
			id := it.str("request_id")
			require.NotEqual(t, reqID1, id)
			require.NotEqual(t, reqID2, id)
		}
	})

	t.Run("R2_P1_1_SingleQuotedAndEscapedSecretsRedactedInSampleAndSearch", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.accounts = h.accountRepo.accounts[:1]
		h.accountRepo.mu.Unlock()

		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusInternalServerError,
			Body:   `{"error":{"type":"server_error","code":"refresh_token='opaque-single-98765'","message":"upstream client_secret='opaque-msg-11223' nested={\"access_token\":\"opaque-esc-33445\"} and token: expired"}}`,
		})

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		for _, secret := range []string{"opaque-single-98765", "opaque-msg-11223", "opaque-esc-33445"} {
			require.NotContains(t, item.str("upstream_error_code"), secret)
			require.NotContains(t, item.str("error_message"), secret)
			searchRes := h.queryRealtime(url.Values{"search": {secret}})
			require.Empty(t, searchRes.Items, "secret %s must not be searchable in realtime samples", secret)
		}
		require.Contains(t, item.str("upstream_error_code"), "refresh_token=***")
		require.Contains(t, item.str("error_message"), "token: expired")
	})

	t.Run("R2_P2_1_CommittedHTTP200StreamingFailoverToTransportError", func(t *testing.T) {
		hijackCloseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}))
		t.Cleanup(hijackCloseSrv.Close)

		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.pingIntervalSeconds = 1
			hc.customKeyMutator = func(_ *e2eAPIKeyRepo, accRepo *e2eAccountRepo, _ *e2eMockUpstream) {
				accRepo.mu.Lock()
				defer accRepo.mu.Unlock()
				for i := range accRepo.accounts {
					if accRepo.accounts[i].ID == 3002 {
						accRepo.accounts[i].Credentials["base_url"] = hijackCloseSrv.URL
					}
				}
			}
		})
		var allowAcquire atomic.Bool
		h.concurrencyCache.mu.Lock()
		h.concurrencyCache.allowUserWait = true
		h.concurrencyCache.onAcquireUserSlot = func(_ context.Context, _ int64, _ int, _ string) (bool, error) {
			return allowAcquire.Load(), nil
		}
		h.concurrencyCache.mu.Unlock()
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusBadGateway,
			Body:   `{"error":{"type":"server_error","code":"err_attempt_1","message":"upstream 1 failed"}}`,
		})

		reqID := uuid.NewString()
		req, err := http.NewRequest(http.MethodPost, h.appServer.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o","stream":true,"input":"hi"}`))
		require.NoError(t, err)
		req.Header.Set("X-Request-Id", reqID)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e2eKeyOpenAI)

		resp, err := h.httpClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		if gotID := strings.TrimSpace(resp.Header.Get("X-Request-Id")); gotID != "" {
			reqID = gotID
		}
		allowAcquire.Store(true)

		rawBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Contains(t, string(rawBody), string(handler.SSEPingFormatComment))
		require.Contains(t, string(rawBody), "response.failed")

		item := h.waitForSample(reqID)
		httpStatus, hasHTTP := item.intVal("http_status")
		require.True(t, hasHTTP && httpStatus == http.StatusOK, "expected http_status=200, got %v", item["http_status"])
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "unknown", item.str("failure_reason"))
		attempts, ok := item.intVal("attempt_count")
		require.True(t, ok && attempts == 2, "expected attempt_count=2, got %v", item["attempt_count"])
		upStatus, hasUpStatus := item.intVal("upstream_status")
		require.True(t, !hasUpStatus || upStatus == 0, "expected upstream_status cleared on committed-200 attempt 2 transport error, got %v", item["upstream_status"])
		require.Empty(t, item.str("upstream_error_code"))
		require.NotEmpty(t, item.str("error_message"))
		require.NotContains(t, item.str("error_message"), "upstream 1 failed")
	})

	t.Run("R2_P2_2_AnthropicSSEInvalidRequestAfterMessageStartKeepsModelReason", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		var antAccounts []service.Account
		for _, acc := range h.accountRepo.accounts {
			if acc.Platform == service.PlatformAnthropic {
				antAccounts = append(antAccounts, acc)
			}
		}
		h.accountRepo.accounts = antAccounts
		h.accountRepo.mu.Unlock()

		sseMidStream := strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"msg_r2_p2_2","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"invalid_request_error","message":"Bad tool input in mid-stream"}}`,
			"",
			"",
		}, "\n")
		h.upstream.setSequence(e2eUpstreamResponse{
			Status:      http.StatusOK,
			ContentType: "text/event-stream",
			Body:        sseMidStream,
		})

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/messages", e2eKeyAnthropic, `{"model":"claude-3-5-sonnet-latest","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, status)

		item := h.waitForSample(reqID)
		httpStatus, ok := item.intVal("http_status")
		require.True(t, ok && httpStatus == http.StatusOK)
		require.Equal(t, "upstream_error", item.str("result"))
		require.Equal(t, "upstream_model_or_request", item.str("failure_reason"))
		require.Equal(t, "invalid_request_error", item.str("upstream_error_code"))

		snap := h.scrapeMetrics()
		require.Equal(t, 1.0, snap.sum("sub2api_upstream_requests_total", "result", "upstream_error", "failure_reason", "upstream_model_or_request"))
		require.Equal(t, 1.0, snap.sum("sub2api_gateway_requests_total", "result", "upstream_error", "failure_reason", "upstream_model_or_request"))
		require.Zero(t, snap.sum("sub2api_gateway_requests_total", "failure_reason", "upstream_auth"))
	})

	t.Run("R2_P2_3_CompositeRewrittenModelPreservesPublicAliasOnFailure", func(t *testing.T) {
		compositeGroupID := int64(2099)
		compKey := "sk-e2e-composite-alias-0099"
		routeRepo := &e2eCompositeRouteRepo{
			routes: []service.CompositeModelRoute{
				{
					ID:             1,
					GroupID:        compositeGroupID,
					PublicModel:    "public-alias-v1",
					MatchType:      service.CompositeRouteMatchExact,
					TargetPlatform: service.PlatformOpenAI,
					UpstreamModel:  "gpt-4o",
					Endpoint:       service.CompositeRouteEndpointResponses,
					Priority:       1,
					Enabled:        true,
				},
			},
		}
		h := newE2EOpsHarness(t, func(hc *e2eHarnessConfig) {
			hc.compositeResolver = service.NewCompositeRouteResolver(routeRepo)
			hc.customKeyMutator = func(keyRepo *e2eAPIKeyRepo, accRepo *e2eAccountRepo, _ *e2eMockUpstream) {
				compGroup := &service.Group{
					ID:               compositeGroupID,
					Name:             "composite-ops-group",
					Platform:         service.PlatformComposite,
					Status:           service.StatusActive,
					SubscriptionType: service.SubscriptionTypeStandard,
					RateMultiplier:   1.0,
					Hydrated:         true,
				}
				keyRepo.mu.RLock()
				alice := keyRepo.keys[e2eKeyOpenAI].User
				keyRepo.mu.RUnlock()
				keyRepo.add(&service.APIKey{
					ID:      1199,
					UserID:  alice.ID,
					Name:    "composite-alias-key",
					Key:     compKey,
					Status:  service.StatusActive,
					User:    alice,
					GroupID: &compositeGroupID,
					Group:   compGroup,
				})
				accRepo.mu.Lock()
				defer accRepo.mu.Unlock()
				accRepo.accounts = accRepo.accounts[:1]
			}
		})
		h.upstream.setSequence(e2eUpstreamResponse{
			Status: http.StatusInternalServerError,
			Body:   `{"error":{"type":"server_error","code":"comp_upstream_500","message":"Composite upstream outage"}}`,
		})

		status, _, _, reqID := h.doHTTP(http.MethodPost, "/v1/responses", compKey, `{"model":"public-alias-v1","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, status)

		item := h.waitForSample(reqID)
		require.Equal(t, "public-alias-v1", item.str("model"))
		modelRes := h.queryRealtime(url.Values{"model": {"public-alias-v1"}})
		require.Len(t, modelRes.Items, 1)
		require.Equal(t, reqID, modelRes.Items[0].str("request_id"))
	})

	t.Run("R2_P2_4_GoogleAndNumericUpstreamErrorCodesCaptured", func(t *testing.T) {
		h := newE2EOpsHarness(t)
		h.accountRepo.mu.Lock()
		h.accountRepo.accounts = h.accountRepo.accounts[:1]
		h.accountRepo.mu.Unlock()

		h.upstream.setSequence(
			e2eUpstreamResponse{
				Status: http.StatusTooManyRequests,
				Body:   `{"error":{"code":429,"message":"Quota exceeded for quota metric","status":"RESOURCE_EXHAUSTED"}}`,
			},
			e2eUpstreamResponse{
				Status: http.StatusInternalServerError,
				Body:   `{"error":{"code":10001,"message":"Numeric upstream error code"}}`,
			},
		)

		st1, _, _, reqID1 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusTooManyRequests, st1)
		item1 := h.waitForSample(reqID1)
		up1, ok1 := item1.intVal("upstream_status")
		require.True(t, ok1 && up1 == http.StatusTooManyRequests)
		require.Equal(t, "RESOURCE_EXHAUSTED", item1.str("upstream_error_code"))

		st2, _, _, reqID2 := h.doHTTP(http.MethodPost, "/v1/responses", e2eKeyOpenAI, `{"model":"gpt-4o","input":"hi"}`)
		require.Equal(t, http.StatusBadGateway, st2)
		item2 := h.waitForSample(reqID2)
		up2, ok2 := item2.intVal("upstream_status")
		require.True(t, ok2 && up2 == http.StatusInternalServerError)
		require.Equal(t, "10001", item2.str("upstream_error_code"))
	})
}

type e2eCompositeRouteRepo struct {
	service.CompositeModelRouteRepository
	routes []service.CompositeModelRoute
}

func (r *e2eCompositeRouteRepo) ListByGroup(_ context.Context, groupID int64, includeDisabled bool) ([]service.CompositeModelRoute, error) {
	out := make([]service.CompositeModelRoute, 0, len(r.routes))
	for _, route := range r.routes {
		if route.GroupID != groupID {
			continue
		}
		if !includeDisabled && !route.Enabled {
			continue
		}
		out = append(out, route)
	}
	return out, nil
}
func (r *e2eCompositeRouteRepo) Create(context.Context, *service.CompositeModelRoute) error { return nil }
func (r *e2eCompositeRouteRepo) Update(context.Context, *service.CompositeModelRoute) error { return nil }
func (r *e2eCompositeRouteRepo) Delete(context.Context, int64) error                        { return nil }
func (r *e2eCompositeRouteRepo) DeleteByGroup(context.Context, int64) error                 { return nil }

// Compile-time unused import guard for bytes (if needed by future tests).
var _ = bytes.NewReader
var _ = sort.Strings
