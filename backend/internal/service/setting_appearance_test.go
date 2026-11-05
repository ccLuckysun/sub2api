//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAppearanceDefaultsAndPartialObject(t *testing.T) {
	defaults := &AppearanceSettings{
		Theme: "system", Accent: "yellow", Motion: "rich", Density: "standard", AllowUserOverride: true,
	}
	require.Equal(t, defaults, DefaultAppearanceSettings())
	got, err := ParseAppearanceSettings([]byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, defaults, got)

	got, err = ParseAppearanceSettings([]byte(`{"accent":"pink","allow_user_override":false}`))
	require.NoError(t, err)
	require.Equal(t, "system", got.Theme)
	require.Equal(t, "pink", got.Accent)
	require.Equal(t, "rich", got.Motion)
	require.Equal(t, "standard", got.Density)
	require.False(t, got.AllowUserOverride)
}

func TestAppearanceExactEnums(t *testing.T) {
	for field, values := range map[string][]string{
		"theme":   {"system", "light", "dark"},
		"accent":  {"yellow", "pink", "blue"},
		"motion":  {"rich", "moderate", "off"},
		"density": {"standard", "compact"},
	} {
		for _, value := range values {
			raw, err := json.Marshal(map[string]string{field: value})
			require.NoError(t, err)
			_, err = ParseAppearanceSettings(raw)
			require.NoError(t, err, "%s=%s", field, value)
		}
	}
}

func TestAppearanceInvalidStoredValuesUseSafeDefaults(t *testing.T) {
	for _, raw := range []string{
		"", "broken", "null", `[]`, `false`, `"dark"`,
		`{"theme":"auto"}`, `{"accent":"red"}`, `{"motion":"reduced"}`, `{"density":"dense"}`,
		`{"theme":null}`, `{"allow_user_override":null}`, `{"allow_user_override":"false"}`,
		`{"theme":"dark","secret":"not-public"}`, `{"Theme":"dark"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			values := map[string]string{SettingKeyAppearance: raw}
			admin := NewSettingService(&settingGetAllRepoStub{values: values}, &config.Config{})
			stored, err := admin.GetAllSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, DefaultAppearanceSettings(), stored.Appearance)

			public := NewSettingService(&settingPublicRepoStub{values: values}, &config.Config{})
			settings, err := public.GetPublicSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, DefaultAppearanceSettings(), settings.Appearance)
			injection, err := public.GetPublicSettingsForInjection(context.Background())
			require.NoError(t, err)
			require.Equal(t, settings.Appearance, injection.(*PublicSettingsInjectionPayload).Appearance)
			encoded, err := json.Marshal(injection)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "not-public")
		})
	}
}

func TestAppearanceRoundTripAndUpdateCallback(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	callbacks := 0
	svc.SetOnUpdateCallback(func() { callbacks++ })
	appearance := &AppearanceSettings{
		Theme: "dark", Accent: "blue", Motion: "off", Density: "compact", AllowUserOverride: false,
	}
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{Appearance: appearance}))
	require.Equal(t, 1, callbacks)
	require.JSONEq(t, `{"theme":"dark","accent":"blue","motion":"off","density":"compact","allow_user_override":false}`,
		repo.updates[SettingKeyAppearance])
	require.Equal(t, appearance, svc.parseSettings(repo.updates).Appearance)

	public := NewSettingService(&settingPublicRepoStub{values: repo.updates}, &config.Config{})
	settings, err := public.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, appearance, settings.Appearance)
	injection, err := public.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	require.Equal(t, appearance, injection.(*PublicSettingsInjectionPayload).Appearance)
}

func TestAppearanceServiceInvalidDoesNotWrite(t *testing.T) {
	for _, field := range []string{"theme", "accent", "motion", "density"} {
		t.Run(field, func(t *testing.T) {
			appearance := DefaultAppearanceSettings()
			switch field {
			case "theme":
				appearance.Theme = "auto"
			case "accent":
				appearance.Accent = "red"
			case "motion":
				appearance.Motion = "reduced"
			case "density":
				appearance.Density = "dense"
			}
			repo := &settingUpdateRepoStub{}
			svc := NewSettingService(repo, &config.Config{})
			callbacks := 0
			svc.SetOnUpdateCallback(func() { callbacks++ })
			for _, withAuthDefaults := range []bool{false, true} {
				settings := &SystemSettings{Appearance: appearance}
				var err error
				if withAuthDefaults {
					err = svc.UpdateSettingsWithAuthSourceDefaults(context.Background(), settings, &AuthSourceDefaultSettings{})
				} else {
					err = svc.UpdateSettings(context.Background(), settings)
				}
				require.ErrorContains(t, err, "appearance."+field)
				require.Nil(t, repo.updates)
				require.Zero(t, callbacks)
			}
		})
	}
}

func TestAppearanceServiceOmissionDoesNotWriteKey(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{}))
	require.NotContains(t, repo.updates, SettingKeyAppearance)
}
