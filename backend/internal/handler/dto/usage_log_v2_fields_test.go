package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogV2DTOOptionalInt64(t *testing.T) {
	zero, body, latency := int64(0), int64(1<<40), int64(1<<35)
	for _, tc := range []struct {
		name          string
		body, latency *int64
	}{
		{"history null", nil, nil}, {"explicit zero", &zero, &zero}, {"int64", &body, &latency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &service.UsageLog{BodyBytes: tc.body, GatewayLatencyMs: tc.latency}
			for _, value := range []any{UsageLogFromService(log), UsageLogFromServiceAdmin(log)} {
				payload, err := json.Marshal(value)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(payload, &fields))
				for key, want := range map[string]*int64{"body_bytes": tc.body, "gateway_latency_ms": tc.latency} {
					raw, ok := fields[key]
					require.True(t, ok, key)
					var got *int64
					require.NoError(t, json.Unmarshal(raw, &got))
					require.Equal(t, want, got, key)
				}
			}
		})
	}
}
