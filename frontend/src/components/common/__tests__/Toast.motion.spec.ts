import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { nextTick } from 'vue'
import Toast from '../Toast.vue'
import { useAppStore } from '@/stores/app'
import { initializeAppearance } from '@/composables/useAppearance'
import { DEFAULT_APPEARANCE } from '@/types/appearance'
import en from '@/i18n/locales/en/common'

let wrapper: VueWrapper | undefined
let pinia: Pinia
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-05T00:00:00Z'))
  localStorage.clear()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  initializeAppearance({ ...DEFAULT_APPEARANCE, motion: 'off' })
  pinia = createPinia()
  setActivePinia(pinia)
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})
function render() {
  wrapper = mount(Toast, {
    attachTo: document.body,
    global: {
      plugins: [pinia, createI18n({ legacy: false, locale: 'en', messages: { en } })],
      stubs: { TransitionGroup: { template: '<div><slot /></div>' } }
    }
  })
}

describe('toast remaining time with motion disabled', () => {
  it('shows an accurate nonanimated countdown until the normal dismissal time', async () => {
    const store = useAppStore()
    store.showSuccess('Saved', 3000)
    render()
    expect(document.querySelector('.toast-progress')).toBeNull()
    expect(document.body.textContent).toContain('Closes in 3s')
    await vi.advanceTimersByTimeAsync(1000)
    await nextTick()
    expect(document.body.textContent).toContain('Closes in 2s')
    expect(document.body.textContent).toContain('Saved')
    await vi.advanceTimersByTimeAsync(2000)
    await nextTick()
    expect(document.body.textContent).not.toContain('Saved')
  })

  it('retains duration-based progress when motion is enabled', () => {
    initializeAppearance(DEFAULT_APPEARANCE)
    useAppStore().showSuccess('Saved', 3000)
    render()
    const progress = document.querySelector<HTMLElement>('.toast-progress')!
    expect(progress.style.animationDuration).toBe('3000ms')
    expect(document.body.textContent).not.toContain('Closes in')
  })
})
