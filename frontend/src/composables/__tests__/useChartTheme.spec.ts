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
    expect(theme.colors.value.surface).toBe('#ffffff')
    expect(theme.tooltip.value.bodyColor).toBe('#111111')
    useAppearance().setPreferences({ theme: 'dark' })
    await nextTick()
    expect(theme.colors.value.surface).toBe('#252525')
    expect(theme.tooltip.value).toMatchObject({ backgroundColor: '#252525', titleColor: '#fff9e9', bodyColor: '#fff9e9', borderWidth: 2 })
  })

  it('resolves CSS tokens instead of hardcoding canvas surfaces', () => {
    document.documentElement.style.setProperty('--neo-surface', '#123456')
    document.documentElement.style.setProperty('--neo-ink', '#abcdef')
    const theme = useChartTheme()
    expect(theme.colors.value.surface).toBe('#123456')
    expect(theme.tooltip.value.bodyColor).toBe('#abcdef')
  })

  it('invalidates accent tokens without recreating the chart', () => {
    const theme = useChartTheme()
    expect(theme.colors.value.accent).toBe('#ffe14d')
    useAppearance().setPreferences({ accent: 'pink' })
    expect(theme.colors.value.accent).toBe('#ff9ecb')
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
