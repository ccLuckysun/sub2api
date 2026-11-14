//go:build unit

package config

import (
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
