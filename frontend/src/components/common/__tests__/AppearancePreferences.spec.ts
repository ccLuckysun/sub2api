import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import AppearancePreferences from '../AppearancePreferences.vue'
import { initializeAppearance, useAppearance } from '@/composables/useAppearance'
import { DEFAULT_APPEARANCE } from '@/types/appearance'
import en from '@/i18n/locales/en/common'

let wrapper: VueWrapper | undefined
function render() {
  wrapper = mount(AppearancePreferences, {
    attachTo: document.body,
    global: {
      plugins: [createI18n({ legacy: false, locale: 'en', messages: { en } })],
      stubs: { transition: true }
    }
  })
  return wrapper
}
async function open() {
  const view = render()
  await view.get('[data-testid="appearance-preferences"]').trigger('click')
  await flushPromises()
  return view
}
async function select(field: string, value: string) {
  const input = document.querySelector<HTMLSelectElement>(`[data-testid="appearance-${field}"]`)!
  input.value = value
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await flushPromises()
}

beforeEach(() => {
  localStorage.clear()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  initializeAppearance(DEFAULT_APPEARANCE)
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('personal appearance controls', () => {
  it('applies each control immediately without submitting site settings', async () => {
    await open()
    await select('theme', 'dark')
    await select('accent', 'pink')
    await select('motion', 'off')
    await select('density', 'compact')
    await flushPromises()
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.dataset).toMatchObject({ accent: 'pink', motion: 'off', density: 'compact' })
    expect(useAppearance().site.value).toEqual(DEFAULT_APPEARANCE)
  })

  it('disables all personal controls when the site disallows overrides', async () => {
    initializeAppearance({ ...DEFAULT_APPEARANCE, allow_user_override: false })
    await open()
    expect(document.querySelector('[role="status"]')?.textContent).toContain('enforced')
    const controls = [...document.querySelectorAll<HTMLSelectElement>('[data-testid^="appearance-"]')].filter(node => node.tagName === 'SELECT')
    expect(controls).toHaveLength(4)
    expect(controls.every(control => control.disabled)).toBe(true)
    await select('accent', 'blue')
    expect(useAppearance().effective.value.accent).toBe('yellow')
  })

  it('restores the administrator defaults with the reset button', async () => {
    initializeAppearance({ ...DEFAULT_APPEARANCE, accent: 'blue' })
    useAppearance().setPreferences({ accent: 'pink', theme: 'dark' })
    await open()
    const reset = [...document.querySelectorAll('button')].find(button => button.textContent?.includes('Use site defaults'))!
    reset.click()
    await flushPromises()
    expect(useAppearance().effective.value.accent).toBe('blue')
    expect(useAppearance().isDark.value).toBe(false)
  })

  it('closes on Escape and restores focus to the opener', async () => {
    const view = render()
    const opener = view.get<HTMLButtonElement>('[data-testid="appearance-preferences"]')
    opener.element.focus()
    await opener.trigger('click')
    await flushPromises()
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(document.activeElement).toBe(opener.element)
  })
})
