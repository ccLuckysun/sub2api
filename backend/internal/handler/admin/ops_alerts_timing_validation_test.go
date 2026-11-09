package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func validTimingAlertPayload(t *testing.T, overrides map[string]any) map[string]json.RawMessage {
	t.Helper()
	payload := map[string]any{
		"name":        "upload p90 slow",
		"metric_type": "upload_ms_p90",
		"operator":    ">",
		"threshold":   8000,
	}
	for k, v := range overrides {
		payload[k] = v
	}
	raw := make(map[string]json.RawMessage, len(payload))
	for k, v := range payload {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		raw[k] = b
	}
	return raw
}

func TestValidateOpsAlertRulePayloadRequestTimingDefaults(t *testing.T) {
	cases := []struct {
		metric   string
		expected int
	}{
		{"upload_ms_p90", 5},
		{"upload_kbps_p50", 10},
		{"large_body_ratio", 5},
		{"real_ttfb_ms_p95", 5},
	}
	for _, tc := range cases {
		raw := validTimingAlertPayload(t, map[string]any{"metric_type": tc.metric, "operator": "<", "threshold": 0.2})
		got, err := validateOpsAlertRulePayload(raw)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.metric, err)
		}
		if got.WindowMinutes != 1 {
			t.Fatalf("%s: window must default to 1, got %d", tc.metric, got.WindowMinutes)
		}
		if got.SustainedMinutes != tc.expected {
			t.Fatalf("%s: sustained default must be %d, got %d", tc.metric, tc.expected, got.SustainedMinutes)
		}
	}
}

func TestValidateOpsAlertRulePayloadRequestTimingRejectsInvalid(t *testing.T) {
	cases := map[string]map[string]any{
		"window":           {"window_minutes": 5},
		"sustained":        {"sustained_minutes": 120},
		"ratio threshold":  {"metric_type": "large_body_ratio", "operator": ">", "threshold": 1.5},
		"min_samples":      {"filters": map[string]any{"min_samples": 0}},
		"non-integer":      {"filters": map[string]any{"min_samples": 30.5}},
		"min_coverage":     {"filters": map[string]any{"min_coverage": 1.5}},
		"recovery_minutes": {"filters": map[string]any{"recovery_minutes": 61}},
	}
	for name, overrides := range cases {
		raw := validTimingAlertPayload(t, overrides)
		if _, err := validateOpsAlertRulePayload(raw); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

func TestValidateOpsAlertRulePayloadKeepsExplicitSustained(t *testing.T) {
	raw := validTimingAlertPayload(t, map[string]any{"sustained_minutes": 3})
	got, err := validateOpsAlertRulePayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SustainedMinutes != 3 {
		t.Fatalf("explicit sustained must win, got %d", got.SustainedMinutes)
	}
}

// 直接调 API 建「请求耗时」规则时，缺省的 enabled/notify_email 必须是 false：
// 与前端模板一致，避免规则一建好就开始发通知。
func TestValidateTimingAlertRuleDefaultsToDisabledAndSilent(t *testing.T) {
	validated, err := validateOpsAlertRulePayload(validTimingAlertPayload(t, nil))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if validated.Enabled {
		t.Fatalf("timing rule must default to disabled")
	}
	if validated.NotifyEmail {
		t.Fatalf("timing rule must default to notify_email=false")
	}
	if validated.EnabledProvided || validated.NotifyProvided {
		t.Fatalf("defaults must not be marked as explicitly provided: enabled=%v notify=%v",
			validated.EnabledProvided, validated.NotifyProvided)
	}
}

// 显式传参优先：显式 true 不得被 timing 默认值覆盖。
func TestValidateTimingAlertRuleKeepsExplicitEnabledAndNotify(t *testing.T) {
	validated, err := validateOpsAlertRulePayload(validTimingAlertPayload(t, map[string]any{
		"enabled":      true,
		"notify_email": true,
	}))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !validated.Enabled || !validated.NotifyEmail {
		t.Fatalf("explicit values lost: enabled=%v notify=%v", validated.Enabled, validated.NotifyEmail)
	}
	explicitOff, err := validateOpsAlertRulePayload(validTimingAlertPayload(t, map[string]any{
		"enabled":      false,
		"notify_email": false,
	}))
	if err != nil {
		t.Fatalf("validate explicit off: %v", err)
	}
	if explicitOff.Enabled || explicitOff.NotifyEmail {
		t.Fatalf("explicit false lost: enabled=%v notify=%v", explicitOff.Enabled, explicitOff.NotifyEmail)
	}
}

// 旧指标不得受新默认影响：缺省仍然是 enabled=true / notify_email=true。
func TestValidateLegacyAlertRuleKeepsDefaultEnabledAndNotify(t *testing.T) {
	raw := map[string]any{
		"name":        "cpu hot",
		"metric_type": "cpu_usage_percent",
		"operator":    ">",
		"threshold":   90,
	}
	bytes := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		bytes[k] = b
	}
	validated, err := validateOpsAlertRulePayload(bytes)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !validated.Enabled || !validated.NotifyEmail {
		t.Fatalf("legacy metric defaults changed: enabled=%v notify=%v", validated.Enabled, validated.NotifyEmail)
	}
}

// 更新时缺省开关必须沿用库里现值：否则 PUT 会把已启用的 timing 规则静默关掉。
func TestApplyTimingAlertUpdateDefaultsKeepsStoredSwitches(t *testing.T) {
	existing := &service.OpsAlertRule{ID: 7, Enabled: true, NotifyEmail: true}
	rule := &service.OpsAlertRule{ID: 7, Enabled: false, NotifyEmail: false}
	validated := &opsAlertRuleValidatedInput{MetricType: "upload_ms_p90"}
	applyTimingAlertUpdateDefaults(rule, validated, existing)
	if !rule.Enabled || !rule.NotifyEmail {
		t.Fatalf("stored switches lost: enabled=%v notify=%v", rule.Enabled, rule.NotifyEmail)
	}

	explicit := &service.OpsAlertRule{ID: 7}
	applyTimingAlertUpdateDefaults(explicit, &opsAlertRuleValidatedInput{
		MetricType:      "upload_ms_p90",
		EnabledProvided: true,
		NotifyProvided:  true,
		Enabled:         false,
		NotifyEmail:     false,
	}, existing)
	if explicit.Enabled || explicit.NotifyEmail {
		t.Fatalf("explicit switches must win: enabled=%v notify=%v", explicit.Enabled, explicit.NotifyEmail)
	}
}
