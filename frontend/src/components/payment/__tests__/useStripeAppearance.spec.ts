import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { initializeAppearance, useAppearance } from '@/composables/useAppearance'
import { DEFAULT_APPEARANCE } from '@/types/appearance'
import { useStripeAppearance } from '../useStripeAppearance'

// Appearance-only checks: no Stripe loader, iframe, order or payment requests.
beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('style')
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  initializeAppearance({ ...DEFAULT_APPEARANCE, theme: 'light' })
})

describe('Stripe appearance', () => {
  it('reacts to theme and accent through a supported appearance object', async () => {
    const appearance = useStripeAppearance()
    expect(appearance.value.theme).toBe('stripe')
    expect(appearance.value.variables?.colorPrimary).toBe('#705400')
    useAppearance().setPreferences({ theme: 'dark', accent: 'blue' })
    await nextTick()
    expect(appearance.value.theme).toBe('night')
    expect(appearance.value.variables).toMatchObject({ colorPrimary: '#9bd0ff', colorBackground: '#252525', colorText: '#fff9e9' })
    expect(appearance.value.rules?.['.Tab--selected']).toMatchObject({ backgroundColor: '#9bd0ff', color: '#111111' })
  })

  it('disables provider input transitions when motion is off', () => {
    const appearance = useStripeAppearance()
    useAppearance().setPreferences({ motion: 'off' })
    expect(appearance.value.rules?.['.Input']?.transition).toBe('none')
  })
})
