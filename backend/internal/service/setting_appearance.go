package service

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// AppearanceSettings contains only the safe, public site appearance defaults.
type AppearanceSettings struct {
	Theme             string `json:"theme"`
	Accent            string `json:"accent"`
	Motion            string `json:"motion"`
	Density           string `json:"density"`
	AllowUserOverride bool   `json:"allow_user_override"`
}

func DefaultAppearanceSettings() *AppearanceSettings {
	return &AppearanceSettings{
		Theme: "system", Accent: "yellow", Motion: "rich", Density: "standard",
		AllowUserOverride: true,
	}
}

func (a *AppearanceSettings) validate() error {
	switch a.Theme {
	case "system", "light", "dark":
	default:
		return fmt.Errorf("appearance.theme must be system, light or dark")
	}
	switch a.Accent {
	case "yellow", "pink", "blue":
	default:
		return fmt.Errorf("appearance.accent must be yellow, pink or blue")
	}
	switch a.Motion {
	case "rich", "moderate", "off":
	default:
		return fmt.Errorf("appearance.motion must be rich, moderate or off")
	}
	switch a.Density {
	case "standard", "compact":
	default:
		return fmt.Errorf("appearance.density must be standard or compact")
	}
	return nil
}

// ParseAppearanceSettings validates a supplied object before any update side
// effects. Omitted members use defaults (not the previous object's values).
// Explicit null, including for individual members, is invalid; false is not an
// omission. Exact keys prevent unknown/case-mismatched fields from being saved.
func ParseAppearanceSettings(raw []byte) (*AppearanceSettings, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("appearance must be an object")
	}
	a := DefaultAppearanceSettings()
	for key, value := range fields {
		var target any
		switch key {
		case "theme":
			target = &a.Theme
		case "accent":
			target = &a.Accent
		case "motion":
			target = &a.Motion
		case "density":
			target = &a.Density
		case "allow_user_override":
			target = &a.AllowUserOverride
		default:
			return nil, fmt.Errorf("unknown appearance field %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("appearance.%s must not be null", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return nil, fmt.Errorf("invalid appearance.%s: %w", key, err)
		}
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

func parseStoredAppearanceSettings(raw string) *AppearanceSettings {
	appearance, err := ParseAppearanceSettings([]byte(raw))
	if err != nil {
		return DefaultAppearanceSettings()
	}
	return appearance
}
