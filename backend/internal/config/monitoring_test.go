//go:build unit

package config

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Defaults: monitoring off with the documented default bind — no config file
// and no env vars means zero behavior change.
func TestMonitoringDefaults(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()
	require.NoError(t, err)

	require.False(t, cfg.Monitoring.MetricsEnabled)
	require.Equal(t, DefaultMetricsBind, cfg.Monitoring.MetricsBind)
}

// Env mapping: MONITORING_METRICS_ENABLED / MONITORING_METRICS_BIND override
// the defaults (keys are SetDefault-registered so AutomaticEnv reaches them).
func TestMonitoringFromEnv(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MONITORING_METRICS_ENABLED", "true")
	t.Setenv("MONITORING_METRICS_BIND", "127.0.0.1:19099")

	cfg, err := Load()
	require.NoError(t, err)

	require.True(t, cfg.Monitoring.MetricsEnabled)
	require.Equal(t, "127.0.0.1:19099", cfg.Monitoring.MetricsBind)
}

// Enabled with a malformed bind must fail validation (fail-fast).
func TestMonitoringValidateBadBind(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MONITORING_METRICS_ENABLED", "true")
	t.Setenv("MONITORING_METRICS_BIND", "not-a-host-port")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "monitoring.metrics_bind")
}

// Disabled with a malformed bind must NOT fail (the bind is never used).
func TestMonitoringValidateBadBindIgnoredWhenDisabled(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()
	require.NoError(t, err)

	cfg.Monitoring.MetricsEnabled = false
	cfg.Monitoring.MetricsBind = "not-a-host-port"
	require.NoError(t, cfg.Validate())
}

// Empty bind falls back to the default.
func TestMonitoringEmptyBindFallsBack(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MONITORING_METRICS_ENABLED", "true")
	t.Setenv("MONITORING_METRICS_BIND", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, DefaultMetricsBind, cfg.Monitoring.MetricsBind)
}

func TestMonitoringV2Defaults(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Monitoring.OpsMetricsEnabled)
	require.False(t, cfg.Monitoring.OpsSamplesEnabled)
	require.Equal(t, 30, cfg.Ops.SampleRetentionDays)
}

func TestMonitoringV2FromEnv(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MONITORING_METRICS_ENABLED", "true")
	t.Setenv("MONITORING_OPS_METRICS_ENABLED", "true")
	t.Setenv("MONITORING_OPS_SAMPLES_ENABLED", "true")
	t.Setenv("OPS_SAMPLE_RETENTION_DAYS", "45")
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Monitoring.OpsMetricsEnabled)
	require.True(t, cfg.Monitoring.OpsSamplesEnabled)
	require.Equal(t, 45, cfg.Ops.SampleRetentionDays)
}

func TestMonitoringV2Dependencies(t *testing.T) {
	for _, tc := range []struct {
		metrics, ops, samples bool
		want                  string
	}{
		{false, false, false, ""}, {true, false, false, ""}, {true, true, false, ""}, {true, true, true, ""},
		{false, true, false, "monitoring.ops_metrics_enabled requires monitoring.metrics_enabled"},
		{false, true, true, "monitoring.ops_metrics_enabled requires monitoring.metrics_enabled"},
		{false, false, true, "monitoring.ops_samples_enabled requires monitoring.ops_metrics_enabled"},
		{true, false, true, "monitoring.ops_samples_enabled requires monitoring.ops_metrics_enabled"},
	} {
		t.Run(fmt.Sprintf("%t_%t_%t", tc.metrics, tc.ops, tc.samples), func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("MONITORING_METRICS_ENABLED", strconv.FormatBool(tc.metrics))
			t.Setenv("MONITORING_OPS_METRICS_ENABLED", strconv.FormatBool(tc.ops))
			t.Setenv("MONITORING_OPS_SAMPLES_ENABLED", strconv.FormatBool(tc.samples))
			_, err := Load()
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestOpsSampleRetention(t *testing.T) {
	for _, days := range []int{-1, 0, 1, 30} {
		t.Run(strconv.Itoa(days), func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("OPS_SAMPLE_RETENTION_DAYS", strconv.Itoa(days))
			cfg, err := Load()
			if days < 0 {
				require.ErrorContains(t, err, "ops.sample_retention_days")
			} else {
				require.NoError(t, err)
				require.Equal(t, days, cfg.Ops.SampleRetentionDays)
			}
		})
	}
}
