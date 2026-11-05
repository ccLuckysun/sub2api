import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APPEARANCE_STORAGE_KEY, configureAppearance, initializeAppearance, useAppearance } from '../useAppearance'
import { DEFAULT_APPEARANCE, normalizeAppearance } from '@/types/appearance'

let dark: MediaQueryList
let reduced: MediaQueryList

function media(matches: boolean): MediaQueryList {
  const events = new EventTarget()
  return {
    matches, media: '', onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(),
    addEventListener: events.addEventListener.bind(events),
    removeEventListener: events.removeEventListener.bind(events),
    dispatchEvent: events.dispatchEvent.bind(events)
  } as MediaQueryList
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.className = ''
  dark = media(false)
  reduced = media(false)
  vi.stubGlobal('matchMedia', (query: string) => query.includes('reduced-motion') ? reduced : dark)
  initializeAppearance(DEFAULT_APPEARANCE)
})

describe('appearance runtime', () => {
  it('uses safe D defaults for invalid or missing settings', () => {
    expect(normalizeAppearance(undefined)).toEqual(DEFAULT_APPEARANCE)
    expect(normalizeAppearance({ theme: 'unknown', accent: '#ff0000', motion: 'wild', density: 0 })).toEqual(DEFAULT_APPEARANCE)
    expect(document.documentElement.dataset).toMatchObject({ accent: 'yellow', motion: 'rich', density: 'standard' })
  })

  it('preserves valid legacy dark preferences on initialization', () => {
    localStorage.setItem('theme', 'dark')
    initializeAppearance(DEFAULT_APPEARANCE)
    expect(useAppearance().isDark.value).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('applies injected site settings before page mount', () => {
    initializeAppearance({ ...DEFAULT_APPEARANCE, theme: 'dark', accent: 'blue', density: 'compact' })
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.dataset.accent).toBe('blue')
    expect(document.documentElement.dataset.density).toBe('compact')
  })

  it('persists personal settings and restores them on initialization', () => {
    const appearance = useAppearance()
    appearance.setPreferences({ accent: 'pink', theme: 'dark', motion: 'moderate' })
    expect(JSON.parse(localStorage.getItem(APPEARANCE_STORAGE_KEY)!)).toMatchObject({ accent: 'pink', theme: 'dark', motion: 'moderate' })
    initializeAppearance(DEFAULT_APPEARANCE)
    expect(appearance.effective.value.accent).toBe('pink')
    expect(appearance.isDark.value).toBe(true)
    expect(document.documentElement.dataset.motion).toBe('moderate')
  })

  it('enforces administrator preferences without erasing personal overrides', () => {
    const appearance = useAppearance()
    appearance.setPreferences({ theme: 'dark', accent: 'pink' })
    configureAppearance({ ...DEFAULT_APPEARANCE, theme: 'light', allow_user_override: false })
    appearance.toggleTheme()
    appearance.setPreferences({ accent: 'blue' })
    expect(appearance.isDark.value).toBe(false)
    expect(appearance.effective.value.accent).toBe('yellow')
    configureAppearance(DEFAULT_APPEARANCE)
    expect(appearance.isDark.value).toBe(true)
    expect(appearance.effective.value.accent).toBe('pink')
  })

  it('always honors reduced motion even when overrides are locked', () => {
    Object.defineProperty(reduced, 'matches', { value: true })
    initializeAppearance({ ...DEFAULT_APPEARANCE, allow_user_override: false })
    expect(document.documentElement.dataset.motion).toBe('off')
    expect(useAppearance().reducedMotion.value).toBe(true)
  })

  it('responds to live system theme and reduced-motion changes', () => {
    Object.defineProperty(dark, 'matches', { value: true, configurable: true })
    dark.dispatchEvent(new Event('change'))
    expect(useAppearance().isDark.value).toBe(true)
    Object.defineProperty(reduced, 'matches', { value: true })
    reduced.dispatchEvent(new Event('change'))
    expect(document.documentElement.dataset.motion).toBe('off')
  })

  it('resets personal preferences to the site defaults', () => {
    configureAppearance({ ...DEFAULT_APPEARANCE, accent: 'blue' })
    useAppearance().setPreferences({ theme: 'dark', accent: 'pink' })
    useAppearance().resetPreferences()
    expect(localStorage.getItem('theme')).toBeNull()
    expect(localStorage.getItem(APPEARANCE_STORAGE_KEY)).toBeNull()
    expect(useAppearance().effective.value.accent).toBe('blue')
    expect(useAppearance().isDark.value).toBe(false)
  })

  it('ignores corrupt stored values and disallows preference escalation', () => {
    localStorage.setItem(APPEARANCE_STORAGE_KEY, '{bad json')
    initializeAppearance(DEFAULT_APPEARANCE)
    expect(useAppearance().effective.value).toEqual(DEFAULT_APPEARANCE)
    localStorage.setItem(APPEARANCE_STORAGE_KEY, JSON.stringify({ allow_user_override: true, accent: 'pink' }))
    initializeAppearance({ ...DEFAULT_APPEARANCE, allow_user_override: false })
    expect(useAppearance().effective.value.allow_user_override).toBe(false)
    expect(useAppearance().effective.value.accent).toBe('yellow')
  })

  it('restores cached site defaults before public settings are available', () => {
    configureAppearance({ ...DEFAULT_APPEARANCE, theme: 'dark', accent: 'blue', allow_user_override: false })
    initializeAppearance()
    expect(useAppearance().isDark.value).toBe(true)
    expect(useAppearance().effective.value.accent).toBe('blue')
    expect(useAppearance().allowUserOverride.value).toBe(false)
  })

  it('prefers an explicit system setting over the legacy theme key', () => {
    localStorage.setItem('theme', 'dark')
    localStorage.setItem(APPEARANCE_STORAGE_KEY, JSON.stringify({ theme: 'system' }))
    initializeAppearance(DEFAULT_APPEARANCE)
    expect(useAppearance().effective.value.theme).toBe('system')
    expect(useAppearance().isDark.value).toBe(false)
  })

  it('synchronizes personal settings across browser tabs', () => {
    localStorage.setItem(APPEARANCE_STORAGE_KEY, JSON.stringify({ theme: 'dark', accent: 'pink' }))
    window.dispatchEvent(new StorageEvent('storage', { key: APPEARANCE_STORAGE_KEY }))
    expect(useAppearance().isDark.value).toBe(true)
    expect(document.documentElement.dataset.accent).toBe('pink')
    localStorage.clear()
    window.dispatchEvent(new StorageEvent('storage', { key: null }))
    expect(useAppearance().isDark.value).toBe(false)
    expect(document.documentElement.dataset.accent).toBe('yellow')
  })

  it('tolerates unavailable storage reads and preference resets', () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    expect(() => initializeAppearance(DEFAULT_APPEARANCE)).not.toThrow()
    get.mockRestore()
    const remove = vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('blocked') })
    expect(() => useAppearance().resetPreferences()).not.toThrow()
    remove.mockRestore()
  })

  it('keeps working when browser storage is unavailable', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    expect(() => configureAppearance(DEFAULT_APPEARANCE)).not.toThrow()
    expect(() => useAppearance().setPreferences({ accent: 'blue' })).not.toThrow()
    expect(document.documentElement.dataset.accent).toBe('blue')
    spy.mockRestore()
  })
})
