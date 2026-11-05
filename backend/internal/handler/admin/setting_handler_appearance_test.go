//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func getAppearanceSettings(t *testing.T, h *SettingHandler) *service.AppearanceSettings {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code)
	return appearanceFromResponse(t, rec)
}

func appearanceFromResponse(t *testing.T, rec *httptest.ResponseRecorder) *service.AppearanceSettings {
	t.Helper()
	var result struct {
		Data dto.SystemSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.NotNil(t, result.Data.Appearance)
	return result.Data.Appearance
}

func TestAdminAppearanceDefaultsRoundTripAndOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.Equal(t, service.DefaultAppearanceSettings(), getAppearanceSettings(t, h))
	callbacks := 0
	h.settingService.SetOnUpdateCallback(func() { callbacks++ })

	want := &service.AppearanceSettings{
		Theme: "light", Accent: "pink", Motion: "moderate", Density: "compact", AllowUserOverride: false,
	}
	rec := doUpdateSettings(t, h, map[string]any{"appearance": want}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, want, appearanceFromResponse(t, rec))
	require.Equal(t, want, getAppearanceSettings(t, h))
	require.Equal(t, 1, callbacks)
	stored := repo.values[service.SettingKeyAppearance]

	rec = doUpdateSettings(t, h, map[string]any{"site_name": "New site"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, want, appearanceFromResponse(t, rec))
	require.Equal(t, stored, repo.values[service.SettingKeyAppearance])
	require.NotContains(t, repo.lastUpdates, service.SettingKeyAppearance)
	require.Equal(t, 2, callbacks)

	// A supplied object replaces the object with defaults for missing members;
	// an omitted boolean defaults true, but an explicit false remains false.
	rec = doUpdateSettings(t, h, map[string]any{"appearance": map[string]any{"theme": "dark"}}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	want = service.DefaultAppearanceSettings()
	want.Theme = "dark"
	require.Equal(t, want, appearanceFromResponse(t, rec))
	rec = doUpdateSettings(t, h, map[string]any{"appearance": map[string]any{"allow_user_override": false}}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	want = service.DefaultAppearanceSettings()
	want.AllowUserOverride = false
	require.Equal(t, want, appearanceFromResponse(t, rec))
}

func TestAdminAppearanceInvalidBeforeAnySideEffects(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `false`, `1`, `"dark"`,
		`{"theme":"auto"}`, `{"theme":""}`, `{"accent":"red"}`, `{"motion":"reduced"}`, `{"density":"dense"}`,
		`{"theme":"DARK"}`, `{"theme":" dark "}`, `{"Theme":"dark"}`,
		`{"theme":null}`, `{"accent":null}`, `{"motion":null}`, `{"density":null}`, `{"allow_user_override":null}`,
		`{"theme":1}`, `{"accent":false}`, `{"motion":[]}`, `{"density":{}}`, `{"allow_user_override":"false"}`,
		`{"allow_user_override":0}`, `{"unknown":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			// No service is wired: invalid appearance must be rejected before
			// any reads (some settings reads migrate), writes, or other services.
			h := NewSettingHandler(nil, nil, nil, nil, nil, nil, nil)
			rec := doUpdateSettings(t, h, map[string]any{
				"appearance": json.RawMessage(raw), "site_name": "must not save",
				"step_up_enabled": true, "payment_enabled": true,
			}, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "appearance")

			// Also assert the normal wired handler leaves all stored values
			// unchanged and never invokes the invalidation callback.
			h, repo := newStepUpSwitchTestHandler(t, map[string]string{
				service.SettingKeySiteName:   "Original",
				service.SettingKeyAppearance: `{"theme":"dark","allow_user_override":false}`,
			})
			before := map[string]string{}
			for key, value := range repo.values {
				before[key] = value
			}
			callbacks := 0
			h.settingService.SetOnUpdateCallback(func() { callbacks++ })
			rec = doUpdateSettings(t, h, map[string]any{
				"appearance": json.RawMessage(raw), "site_name": "must not save",
			}, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, before, repo.values)
			require.Nil(t, repo.lastUpdates)
			require.Zero(t, callbacks)
		})
	}
}

func TestAdminAppearanceAuditChangedKey(t *testing.T) {
	before := &service.SystemSettings{Appearance: service.DefaultAppearanceSettings()}
	after := &service.SystemSettings{Appearance: service.DefaultAppearanceSettings()}
	require.NotContains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), "appearance")
	after.Appearance.AllowUserOverride = false
	require.Contains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), "appearance")
}
