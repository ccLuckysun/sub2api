import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { initializeAppearance, useAppearance } from '../useAppearance'
import { useChartTheme } from '../useChartTheme'
import { DEFAULT_APPEARANCE } from '@/types/appearance'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('style')
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  initializeAppearance({ ...DEFAULT_APPEARANCE, theme: 'light' })
})

describe('canvas appearance', () => {
  it('updates text, surfaces and tooltips on a live theme change', async () => {
    const theme = useChartTheme()
    expect(theme.colors.value.surface).toBe('#FFFFFF')
    expect(theme.tooltip.value.bodyColor).toBe('#202126')
    useAppearance().setPreferences({ theme: 'dark' })
    await nextTick()
    expect(theme.colors.value.surface).toBe('#111216')
    expect(theme.tooltip.value).toMatchObject({ backgroundColor: '#111216', titleColor: '#ECE8E1', bodyColor: '#ECE8E1', borderWidth: 1 })
  })

  it('resolves CSS tokens instead of hardcoding canvas surfaces', () => {
    document.documentElement.style.setProperty('--ui-surface', '#123456')
    document.documentElement.style.setProperty('--ui-ink', '#abcdef')
    const theme = useChartTheme()
    expect(theme.colors.value.surface).toBe('#123456')
    expect(theme.tooltip.value.bodyColor).toBe('#abcdef')
  })

  it('invalidates accent tokens without recreating the chart', () => {
    const theme = useChartTheme()
    expect(theme.colors.value.accent).toBe('#D5BEA4')
    useAppearance().setPreferences({ accent: 'pink' })
    expect(theme.colors.value.accent).toBe('#C5B9EE')
    useAppearance().setPreferences({ accent: 'blue' })
    expect(theme.colors.value.accent).toBe('#BAD9E5')
  })

  it('re-reads ink and grid tokens after an appearance change', () => {
    const theme = useChartTheme()
    expect(theme.colors.value.grid).toBe('#D5D6DC')
    document.documentElement.style.setProperty('--ui-border', '#34353d')
    document.documentElement.style.setProperty('--ui-muted', '#a6a19a')
    useAppearance().setPreferences({ theme: 'dark' })
    expect(theme.colors.value.grid).toBe('#34353d')
    expect(theme.tooltip.value.borderColor).toBe('#34353d')
    expect(theme.colors.value.muted).toBe('#a6a19a')
  })

  it('keeps the administrator-selected preset when user overrides are locked', () => {
    initializeAppearance({ ...DEFAULT_APPEARANCE, theme: 'dark', accent: 'blue', allow_user_override: false })
    const theme = useChartTheme()
    useAppearance().setPreferences({ theme: 'light', accent: 'pink' })
    expect(theme.colors.value.surface).toBe('#111216')
    expect(theme.colors.value.accent).toBe('#BAD9E5')
  })

  it('turns Chart.js animation off and limits moderate motion', () => {
    const theme = useChartTheme()
    expect(theme.animation.value).toEqual({ duration: 300 })
    useAppearance().setPreferences({ motion: 'moderate' })
    expect(theme.animation.value).toEqual({ duration: 120 })
    useAppearance().setPreferences({ motion: 'off' })
    expect(theme.animation.value).toBe(false)
  })

  it('honors system reduced motion even when the site locks preferences', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('reduced-motion'), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
    initializeAppearance({ ...DEFAULT_APPEARANCE, motion: 'rich', allow_user_override: false })
    expect(useChartTheme().animation.value).toBe(false)
  })
})
