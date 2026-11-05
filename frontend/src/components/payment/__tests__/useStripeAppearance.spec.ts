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
    expect(appearance.value.variables?.colorPrimary).toBe('#785C42')
    useAppearance().setPreferences({ theme: 'dark', accent: 'blue' })
    await nextTick()
    expect(appearance.value.theme).toBe('night')
    expect(appearance.value.variables).toMatchObject({ colorPrimary: '#BAD9E5', colorBackground: '#111216', colorText: '#ECE8E1' })
    expect(appearance.value.rules?.['.Tab--selected']).toMatchObject({ backgroundColor: '#BAD9E5', color: '#191715', boxShadow: 'none' })
  })

  it('uses fine borders and a non-offset focus ring', () => {
    const appearance = useStripeAppearance()
    expect(appearance.value.rules?.['.Input']?.border).toBe('1px solid #D5D6DC')
    expect(appearance.value.rules?.['.Input:focus']?.boxShadow).toBe('0 0 0 1px #785C42')
    expect(appearance.value.rules?.['.Label']?.fontWeight).toBe('500')
  })

  it('honors a resolved semantic focus token', () => {
    document.documentElement.style.setProperty('--ui-ring', '#694F8C')
    const appearance = useStripeAppearance()
    expect(appearance.value.variables?.colorPrimary).toBe('#694F8C')
    expect(appearance.value.rules?.['.Input:focus']?.borderColor).toBe('#694F8C')
  })

  it('disables provider input transitions when motion is off', () => {
    const appearance = useStripeAppearance()
    useAppearance().setPreferences({ motion: 'off' })
    expect(appearance.value.rules?.['.Input']?.transition).toBe('none')
  })
})
