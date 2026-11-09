package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

var validOpsAlertMetricTypes = []string{
	"success_rate",
	"error_rate",
	"upstream_error_rate",
	"cpu_usage_percent",
	"memory_usage_percent",
	"concurrency_queue_depth",
	"group_available_accounts",
	"group_available_ratio",
	"group_rate_limit_ratio",
	"account_rate_limited_count",
	"account_error_count",
	"account_error_ratio",
	"account_temp_unscheduled_count",
	"overload_account_count",
	"proxy_expired_count",
	"proxy_expiring_soon_count",
	"upload_ms_p90",
	"upload_kbps_p50",
	"large_body_ratio",
	"real_ttfb_ms_p95",
}

var validOpsAlertMetricTypeSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(validOpsAlertMetricTypes))
	for _, v := range validOpsAlertMetricTypes {
		set[v] = struct{}{}
	}
	return set
}()

var validOpsAlertOperators = []string{">", "<", ">=", "<=", "==", "!="}

var validOpsAlertOperatorSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(validOpsAlertOperators))
	for _, v := range validOpsAlertOperators {
		set[v] = struct{}{}
	}
	return set
}()

var validOpsAlertSeverities = []string{"P0", "P1", "P2", "P3"}

var validOpsAlertSeveritySet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(validOpsAlertSeverities))
	for _, v := range validOpsAlertSeverities {
		set[v] = struct{}{}
	}
	return set
}()

type opsAlertRuleValidatedInput struct {
	Name       string
	MetricType string
	Operator   string
	Threshold  float64

	Severity string

	WindowMinutes    int
	SustainedMinutes int
	CooldownMinutes  int

	Enabled     bool
	NotifyEmail bool

	WindowProvided    bool
	SustainedProvided bool
	CooldownProvided  bool
	SeverityProvided  bool
	EnabledProvided   bool
	NotifyProvided    bool
}

func isPercentOrRateMetric(metricType string) bool {
	switch metricType {
	case "success_rate",
		"error_rate",
		"upstream_error_rate",
		"cpu_usage_percent",
		"memory_usage_percent",
		"group_available_ratio",
		"group_rate_limit_ratio",
		"account_error_ratio":
		return true
	default:
		return false
	}
}

func validateOpsAlertRulePayload(raw map[string]json.RawMessage) (*opsAlertRuleValidatedInput, error) {
	if raw == nil {
		return nil, fmt.Errorf("invalid request body")
	}

	requiredFields := []string{"name", "metric_type", "operator", "threshold"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			return nil, fmt.Errorf("%s is required", field)
		}
	}

	var name string
	if err := json.Unmarshal(raw["name"], &name); err != nil || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	name = strings.TrimSpace(name)

	var metricType string
	if err := json.Unmarshal(raw["metric_type"], &metricType); err != nil || strings.TrimSpace(metricType) == "" {
		return nil, fmt.Errorf("metric_type is required")
	}
	metricType = strings.TrimSpace(metricType)
	if _, ok := validOpsAlertMetricTypeSet[metricType]; !ok {
		return nil, fmt.Errorf("metric_type must be one of: %s", strings.Join(validOpsAlertMetricTypes, ", "))
	}

	var operator string
	if err := json.Unmarshal(raw["operator"], &operator); err != nil || strings.TrimSpace(operator) == "" {
		return nil, fmt.Errorf("operator is required")
	}
	operator = strings.TrimSpace(operator)
	if _, ok := validOpsAlertOperatorSet[operator]; !ok {
		return nil, fmt.Errorf("operator must be one of: %s", strings.Join(validOpsAlertOperators, ", "))
	}

	var threshold float64
	if err := json.Unmarshal(raw["threshold"], &threshold); err != nil {
		return nil, fmt.Errorf("threshold must be a number")
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return nil, fmt.Errorf("threshold must be a finite number")
	}
	if isPercentOrRateMetric(metricType) {
		if threshold < 0 || threshold > 100 {
			return nil, fmt.Errorf("threshold must be between 0 and 100 for metric_type %s", metricType)
		}
	} else if threshold < 0 {
		return nil, fmt.Errorf("threshold must be >= 0")
	}

	if metricType == "large_body_ratio" {
		if threshold < 0 || threshold > 1 {
			return nil, fmt.Errorf("threshold must be between 0 and 1 for metric_type %s", metricType)
		}
	}

	validated := &opsAlertRuleValidatedInput{
		Name:       name,
		MetricType: metricType,
		Operator:   operator,
		Threshold:  threshold,
	}

	if v, ok := raw["severity"]; ok {
		validated.SeverityProvided = true
		var sev string
		if err := json.Unmarshal(v, &sev); err != nil {
			return nil, fmt.Errorf("severity must be a string")
		}
		sev = strings.ToUpper(strings.TrimSpace(sev))
		if sev != "" {
			if _, ok := validOpsAlertSeveritySet[sev]; !ok {
				return nil, fmt.Errorf("severity must be one of: %s", strings.Join(validOpsAlertSeverities, ", "))
			}
			validated.Severity = sev
		}
	}
	if validated.Severity == "" {
		validated.Severity = "P2"
	}

	if v, ok := raw["enabled"]; ok {
		validated.EnabledProvided = true
		if err := json.Unmarshal(v, &validated.Enabled); err != nil {
			return nil, fmt.Errorf("enabled must be a boolean")
		}
	} else {
		validated.Enabled = true
	}

	if v, ok := raw["notify_email"]; ok {
		validated.NotifyProvided = true
		if err := json.Unmarshal(v, &validated.NotifyEmail); err != nil {
			return nil, fmt.Errorf("notify_email must be a boolean")
		}
	} else {
		validated.NotifyEmail = true
	}

	if v, ok := raw["window_minutes"]; ok {
		validated.WindowProvided = true
		if err := json.Unmarshal(v, &validated.WindowMinutes); err != nil {
			return nil, fmt.Errorf("window_minutes must be an integer")
		}
		switch validated.WindowMinutes {
		case 1, 5, 60:
		default:
			return nil, fmt.Errorf("window_minutes must be one of: 1, 5, 60")
		}
	} else {
		validated.WindowMinutes = 1
	}

	if v, ok := raw["sustained_minutes"]; ok {
		validated.SustainedProvided = true
		if err := json.Unmarshal(v, &validated.SustainedMinutes); err != nil {
			return nil, fmt.Errorf("sustained_minutes must be an integer")
		}
		if validated.SustainedMinutes < 1 || validated.SustainedMinutes > 1440 {
			return nil, fmt.Errorf("sustained_minutes must be between 1 and 1440")
		}
	} else {
		validated.SustainedMinutes = 1
	}

	if v, ok := raw["cooldown_minutes"]; ok {
		validated.CooldownProvided = true
		if err := json.Unmarshal(v, &validated.CooldownMinutes); err != nil {
			return nil, fmt.Errorf("cooldown_minutes must be an integer")
		}
		if validated.CooldownMinutes < 0 || validated.CooldownMinutes > 1440 {
			return nil, fmt.Errorf("cooldown_minutes must be between 0 and 1440")
		}
	} else {
		validated.CooldownMinutes = 0
	}

	if err := validateRequestTimingAlertRule(raw, validated); err != nil {
		return nil, err
	}

	return validated, nil
}

// validateRequestTimingAlertRule enforces the request-timing alert contract: a fixed
// one-minute window (the evaluation unit is a closed minute bucket), a bounded
// sustained streak, a 0..1 threshold for the ratio metric, and sanity-checked filters.
func validateRequestTimingAlertRule(raw map[string]json.RawMessage, validated *opsAlertRuleValidatedInput) error {
	if validated == nil || !service.IsRequestTimingMetric(validated.MetricType) {
		return nil
	}
	if validated.WindowProvided && validated.WindowMinutes != 1 {
		return fmt.Errorf("window_minutes must be 1 for metric_type %s", validated.MetricType)
	}
	validated.WindowMinutes = 1
	// 新 timing 模板默认停用且不发邮件：直接调 API 建规则时同样如此，
	// 避免「建好即开始告警」。显式传参优先；旧指标仍沿用通用默认 true/true。
	if !validated.EnabledProvided {
		validated.Enabled = false
	}
	if !validated.NotifyProvided {
		validated.NotifyEmail = false
	}
	if !validated.SustainedProvided {
		validated.SustainedMinutes = service.DefaultRequestTimingSustainedMinutes(validated.MetricType)
	}
	if validated.SustainedMinutes < 1 || validated.SustainedMinutes > service.TimingAlertMaxSustainedMinutes {
		return fmt.Errorf("sustained_minutes must be between 1 and %d for metric_type %s", service.TimingAlertMaxSustainedMinutes, validated.MetricType)
	}
	filters, err := decodeAlertFilters(raw)
	if err != nil {
		return err
	}
	if filters == nil {
		return nil
	}
	if v, ok := filters["min_samples"]; ok {
		n, ok := alertFilterNumber(v)
		if !ok || n < 1 || n > 100000 || math.Trunc(n) != n {
			return fmt.Errorf("filters.min_samples must be an integer between 1 and 100000")
		}
	}
	if v, ok := filters["min_coverage"]; ok {
		n, ok := alertFilterNumber(v)
		if !ok || n <= 0 || n > 1 {
			return fmt.Errorf("filters.min_coverage must be greater than 0 and at most 1")
		}
	}
	if v, ok := filters["recovery_minutes"]; ok {
		n, ok := alertFilterNumber(v)
		if !ok || n < 0 || n > 60 || math.Trunc(n) != n {
			return fmt.Errorf("filters.recovery_minutes must be an integer between 0 and 60")
		}
	}
	return nil
}

func decodeAlertFilters(raw map[string]json.RawMessage) (map[string]any, error) {
	v, ok := raw["filters"]
	if !ok || len(v) == 0 || string(v) == "null" {
		return nil, nil
	}
	var filters map[string]any
	if err := json.Unmarshal(v, &filters); err != nil {
		return nil, fmt.Errorf("filters must be an object")
	}
	return filters, nil
}

func alertFilterNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

// ListAlertRules returns all ops alert rules.
// GET /api/v1/admin/ops/alert-rules
func (h *OpsHandler) ListAlertRules(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rules, err := h.opsService.ListAlertRules(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.attachRequestTimingEvaluations(c.Request.Context(), rules)
	response.Success(c, rules)
}

// attachRequestTimingEvaluations decorates request-timing rules with their persisted
// evaluation snapshot in one batched read. An evaluation that never ran (or that is
// unknown/stale/insufficient) is reported as such instead of being omitted, so the UI
// cannot present a missing evaluation as healthy.
func (h *OpsHandler) attachRequestTimingEvaluations(ctx context.Context, rules []*service.OpsAlertRule) {
	if h.opsService == nil || len(rules) == 0 {
		return
	}
	ids := make([]int64, 0, len(rules))
	for _, rule := range rules {
		if rule == nil || rule.ID <= 0 || !service.IsRequestTimingMetric(rule.MetricType) {
			continue
		}
		ids = append(ids, rule.ID)
	}
	if len(ids) == 0 {
		return
	}
	states, err := h.opsService.RequestTimingAlertEvaluations(ctx, ids)
	if err != nil {
		// A failed snapshot must not break the rule list; the rules keep no evaluation
		// and the UI shows "not evaluated yet" rather than a synthetic healthy state.
		logger.LegacyPrintf("handler.ops_alerts", "[OpsAlerts] load request timing evaluations failed: %v", err)
		return
	}
	for _, rule := range rules {
		if rule == nil || rule.ID <= 0 || !service.IsRequestTimingMetric(rule.MetricType) {
			continue
		}
		state, ok := states[rule.ID]
		if !ok || state == nil || strings.TrimSpace(state.State) == "" {
			continue
		}
		// Snapshot decoration downgrades a verdict the evaluator stopped refreshing, so
		// "the list endpoint read a row" is never mistaken for "the rule is healthy now".
		rule.Evaluation = service.TimingAlertEvaluationSnapshot(state, time.Now())
	}
}

// applyTimingAlertUpdateDefaults keeps the stored switch state for an update that omits
// enabled/notify_email. Creation still defaults to disabled+silent (see
// validateOpsAlertRulePayload); only the update path inherits the existing values.
func applyTimingAlertUpdateDefaults(rule *service.OpsAlertRule, validated *opsAlertRuleValidatedInput, existing *service.OpsAlertRule) {
	if rule == nil || validated == nil || existing == nil {
		return
	}
	if !validated.EnabledProvided {
		rule.Enabled = existing.Enabled
	}
	if !validated.NotifyProvided {
		rule.NotifyEmail = existing.NotifyEmail
	}
}

// CreateAlertRule creates an ops alert rule.
// POST /api/v1/admin/ops/alert-rules
func (h *OpsHandler) CreateAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var raw map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&raw, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	validated, err := validateOpsAlertRulePayload(raw)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var rule service.OpsAlertRule
	if err := c.ShouldBindBodyWith(&rule, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	rule.Name = validated.Name
	rule.MetricType = validated.MetricType
	rule.Operator = validated.Operator
	rule.Threshold = validated.Threshold
	rule.WindowMinutes = validated.WindowMinutes
	rule.SustainedMinutes = validated.SustainedMinutes
	rule.CooldownMinutes = validated.CooldownMinutes
	rule.Severity = validated.Severity
	rule.Enabled = validated.Enabled
	rule.NotifyEmail = validated.NotifyEmail

	created, err := h.opsService.CreateAlertRule(c.Request.Context(), &rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// UpdateAlertRule updates an existing ops alert rule.
// PUT /api/v1/admin/ops/alert-rules/:id
func (h *OpsHandler) UpdateAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	var raw map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&raw, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	validated, err := validateOpsAlertRulePayload(raw)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var rule service.OpsAlertRule
	if err := c.ShouldBindBodyWith(&rule, binding.JSON); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	rule.ID = id
	rule.Name = validated.Name
	rule.MetricType = validated.MetricType
	rule.Operator = validated.Operator
	rule.Threshold = validated.Threshold
	rule.WindowMinutes = validated.WindowMinutes
	rule.SustainedMinutes = validated.SustainedMinutes
	rule.CooldownMinutes = validated.CooldownMinutes
	rule.Severity = validated.Severity
	rule.Enabled = validated.Enabled
	rule.NotifyEmail = validated.NotifyEmail
	// timing 规则的缺省值是 false（见 validateOpsAlertRulePayload）。更新时若客户端没带
	// enabled/notify_email，必须沿用库里现值，否则一次 PUT 会把已启用的规则静默关掉。
	if service.IsRequestTimingMetric(rule.MetricType) && (!validated.EnabledProvided || !validated.NotifyProvided) {
		if existing, listErr := h.opsService.ListAlertRules(c.Request.Context()); listErr == nil {
			for _, candidate := range existing {
				if candidate != nil && candidate.ID == id {
					applyTimingAlertUpdateDefaults(&rule, validated, candidate)
					break
				}
			}
		}
	}

	updated, err := h.opsService.UpdateAlertRule(c.Request.Context(), &rule)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// DeleteAlertRule deletes an ops alert rule.
// DELETE /api/v1/admin/ops/alert-rules/:id
func (h *OpsHandler) DeleteAlertRule(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	if err := h.opsService.DeleteAlertRule(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// GetAlertEvent returns a single ops alert event.
// GET /api/v1/admin/ops/alert-events/:id
func (h *OpsHandler) GetAlertEvent(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}

	ev, err := h.opsService.GetAlertEventByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, ev)
}

// UpdateAlertEventStatus updates an ops alert event status.
// PUT /api/v1/admin/ops/alert-events/:id/status
func (h *OpsHandler) UpdateAlertEventStatus(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}

	var payload struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	payload.Status = strings.TrimSpace(payload.Status)
	if payload.Status == "" {
		response.BadRequest(c, "Invalid status")
		return
	}
	if payload.Status != service.OpsAlertStatusResolved && payload.Status != service.OpsAlertStatusManualResolved {
		response.BadRequest(c, "Invalid status")
		return
	}

	var resolvedAt *time.Time
	if payload.Status == service.OpsAlertStatusResolved || payload.Status == service.OpsAlertStatusManualResolved {
		now := time.Now().UTC()
		resolvedAt = &now
	}
	if err := h.opsService.UpdateAlertEventStatus(c.Request.Context(), id, payload.Status, resolvedAt); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"updated": true})
}

// ListAlertEvents lists recent ops alert events.
// GET /api/v1/admin/ops/alert-events
// CreateAlertSilence creates a scoped silence for ops alerts.
// POST /api/v1/admin/ops/alert-silences
func (h *OpsHandler) CreateAlertSilence(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var payload struct {
		RuleID   int64   `json:"rule_id"`
		Platform string  `json:"platform"`
		GroupID  *int64  `json:"group_id"`
		Region   *string `json:"region"`
		Until    string  `json:"until"`
		Reason   string  `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.Until))
	if err != nil {
		response.BadRequest(c, "Invalid until")
		return
	}

	createdBy := (*int64)(nil)
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		uid := subject.UserID
		createdBy = &uid
	}

	silence := &service.OpsAlertSilence{
		RuleID:    payload.RuleID,
		Platform:  strings.TrimSpace(payload.Platform),
		GroupID:   payload.GroupID,
		Region:    payload.Region,
		Until:     until,
		Reason:    strings.TrimSpace(payload.Reason),
		CreatedBy: createdBy,
	}

	created, err := h.opsService.CreateAlertSilence(c.Request.Context(), silence)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

func (h *OpsHandler) ListAlertEvents(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			response.BadRequest(c, "Invalid limit")
			return
		}
		limit = n
	}

	filter := &service.OpsAlertEventFilter{
		Limit:    limit,
		Status:   strings.TrimSpace(c.Query("status")),
		Severity: strings.TrimSpace(c.Query("severity")),
	}

	if v := strings.TrimSpace(c.Query("email_sent")); v != "" {
		vv := strings.ToLower(v)
		switch vv {
		case "true", "1":
			b := true
			filter.EmailSent = &b
		case "false", "0":
			b := false
			filter.EmailSent = &b
		default:
			response.BadRequest(c, "Invalid email_sent")
			return
		}
	}

	// Cursor pagination: both params must be provided together.
	rawTS := strings.TrimSpace(c.Query("before_fired_at"))
	rawID := strings.TrimSpace(c.Query("before_id"))
	if (rawTS == "") != (rawID == "") {
		response.BadRequest(c, "before_fired_at and before_id must be provided together")
		return
	}
	if rawTS != "" {
		ts, err := time.Parse(time.RFC3339Nano, rawTS)
		if err != nil {
			if t2, err2 := time.Parse(time.RFC3339, rawTS); err2 == nil {
				ts = t2
			} else {
				response.BadRequest(c, "Invalid before_fired_at")
				return
			}
		}
		filter.BeforeFiredAt = &ts
	}
	if rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid before_id")
			return
		}
		filter.BeforeID = &id
	}

	// Optional global filter support (platform/group/time range).
	if platform := strings.TrimSpace(c.Query("platform")); platform != "" {
		filter.Platform = platform
	}
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		filter.GroupID = &id
	}
	if startTime, endTime, err := parseOpsTimeRange(c, "24h"); err == nil {
		// Only apply when explicitly provided to avoid surprising default narrowing.
		if strings.TrimSpace(c.Query("start_time")) != "" || strings.TrimSpace(c.Query("end_time")) != "" || strings.TrimSpace(c.Query("time_range")) != "" {
			filter.StartTime = &startTime
			filter.EndTime = &endTime
		}
	} else {
		response.BadRequest(c, err.Error())
		return
	}

	events, err := h.opsService.ListAlertEvents(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, events)
}
