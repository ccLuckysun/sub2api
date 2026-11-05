import { computed, readonly, ref } from 'vue'
import {
  DEFAULT_APPEARANCE, normalizeAppearance, normalizePreferences,
  type AppearancePreferences, type AppearanceSettings
} from '@/types/appearance'

export const APPEARANCE_STORAGE_KEY = 'sub2api.appearance.v1'
const SITE_STORAGE_KEY = 'sub2api.appearance.site.v1'
const site = ref<AppearanceSettings>({ ...DEFAULT_APPEARANCE })
const preferences = ref<AppearancePreferences>({})
const systemDark = ref(false)
const reducedMotion = ref(false)
let removeListeners: (() => void) | undefined

function read(key: string): unknown {
  try {
    const value = localStorage.getItem(key)
    return value ? JSON.parse(value) : undefined
  } catch { return undefined }
}

function persist(key: string, value: unknown): void {
  try { localStorage.setItem(key, JSON.stringify(value)) } catch { /* Private browsing / quota: keep in-memory controls usable. */ }
}

function loadPreferences(): AppearancePreferences {
  const value = normalizePreferences(read(APPEARANCE_STORAGE_KEY))
  try {
    const legacy = localStorage.getItem('theme')
    if (!value.theme && (legacy === 'dark' || legacy === 'light')) value.theme = legacy
  } catch { /* Storage unavailable. */ }
  return value
}

const effective = computed<AppearanceSettings>(() => ({
  ...site.value,
  ...(site.value.allow_user_override ? preferences.value : {})
}))
const isDark = computed(() => effective.value.theme === 'dark' ||
  (effective.value.theme === 'system' && systemDark.value))
const motion = computed(() => reducedMotion.value ? 'off' : effective.value.motion)

function apply(): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.classList.toggle('dark', isDark.value)
  root.dataset.accent = effective.value.accent
  root.dataset.motion = motion.value
  root.dataset.density = effective.value.density
  root.style.colorScheme = isDark.value ? 'dark' : 'light'
}

export function configureAppearance(value: unknown): void {
  site.value = normalizeAppearance(value)
  persist(SITE_STORAGE_KEY, site.value)
  apply()
}

/** Called before mounting, shared by every route (including auth / custom home). */
export function initializeAppearance(value?: unknown): void {
  removeListeners?.()
  preferences.value = loadPreferences()
  site.value = normalizeAppearance(value ?? read(SITE_STORAGE_KEY))
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    const dark = window.matchMedia('(prefers-color-scheme: dark)')
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => {
      systemDark.value = dark.matches
      reducedMotion.value = reduced.matches
      apply()
    }
    const storage = (event: StorageEvent) => {
      if (event.key === null || event.key === APPEARANCE_STORAGE_KEY || event.key === 'theme') {
        preferences.value = loadPreferences()
        apply()
      }
    }
    dark.addEventListener?.('change', sync)
    reduced.addEventListener?.('change', sync)
    window.addEventListener('storage', storage)
    removeListeners = () => {
      dark.removeEventListener?.('change', sync)
      reduced.removeEventListener?.('change', sync)
      window.removeEventListener('storage', storage)
    }
    sync()
  } else {
    apply()
  }
}

function setPreferences(value: AppearancePreferences): void {
  if (!site.value.allow_user_override) return
  preferences.value = { ...preferences.value, ...normalizePreferences(value) }
  persist(APPEARANCE_STORAGE_KEY, preferences.value)
  // Keep older integrations that still read this key compatible.
  try {
    if (preferences.value.theme && preferences.value.theme !== 'system') localStorage.setItem('theme', preferences.value.theme)
    else localStorage.removeItem('theme')
  } catch { /* Storage unavailable. */ }
  apply()
}

function resetPreferences(): void {
  preferences.value = {}
  try {
    localStorage.removeItem(APPEARANCE_STORAGE_KEY)
    localStorage.removeItem('theme')
  } catch { /* Storage unavailable. */ }
  apply()
}

export function useAppearance() {
  return {
    site: readonly(site), preferences: readonly(preferences), effective, isDark, motion,
    reducedMotion: readonly(reducedMotion),
    allowUserOverride: computed(() => site.value.allow_user_override),
    setPreferences, resetPreferences,
    toggleTheme: () => setPreferences({ theme: isDark.value ? 'light' : 'dark' })
  }
}
