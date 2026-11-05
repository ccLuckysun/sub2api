export type AppearanceTheme = 'system' | 'light' | 'dark'
export type AppearanceAccent = 'yellow' | 'pink' | 'blue'
export type AppearanceMotion = 'rich' | 'moderate' | 'off'
export type AppearanceDensity = 'standard' | 'compact'

export interface AppearanceSettings {
  theme: AppearanceTheme
  accent: AppearanceAccent
  motion: AppearanceMotion
  density: AppearanceDensity
  allow_user_override: boolean
}

export type AppearancePreferences = Partial<Omit<AppearanceSettings, 'allow_user_override'>>

export const DEFAULT_APPEARANCE: Readonly<AppearanceSettings> = Object.freeze({
  theme: 'system',
  accent: 'yellow',
  motion: 'rich',
  density: 'standard',
  allow_user_override: true
})

export function normalizePreferences(value: unknown): AppearancePreferences {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const source = value as Record<string, unknown>
  const result: AppearancePreferences = {}
  if (source.theme === 'system' || source.theme === 'light' || source.theme === 'dark') result.theme = source.theme
  if (source.accent === 'yellow' || source.accent === 'pink' || source.accent === 'blue') result.accent = source.accent
  if (source.motion === 'rich' || source.motion === 'moderate' || source.motion === 'off') result.motion = source.motion
  if (source.density === 'standard' || source.density === 'compact') result.density = source.density
  return result
}

export function normalizeAppearance(value: unknown): AppearanceSettings {
  const source = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  return {
    ...DEFAULT_APPEARANCE,
    ...normalizePreferences(value),
    allow_user_override: typeof source.allow_user_override === 'boolean'
      ? source.allow_user_override : DEFAULT_APPEARANCE.allow_user_override
  }
}
