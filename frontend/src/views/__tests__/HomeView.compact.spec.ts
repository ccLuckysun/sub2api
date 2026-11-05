import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, RouterLinkStub, type VueWrapper } from '@vue/test-utils'
import HomeView from '../HomeView.vue'
import { configureAppearance, initializeAppearance } from '@/composables/useAppearance'

const { appStore, authStore, setDeepening, replay } = vi.hoisted(() => ({
  appStore: { cachedPublicSettings: {} as Record<string, unknown>, siteName: 'Fallback site', siteLogo: '', docUrl: '', publicSettingsLoaded: true, fetchPublicSettings: vi.fn() },
  authStore: { isAuthenticated: false, isAdmin: false, user: null as { email?: string } | null, checkAuth: vi.fn() },
  setDeepening: vi.fn(), replay: vi.fn()
}))
vi.mock('@/stores', () => ({ useAppStore: () => appStore, useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/home/BlackHoleScene.vue', () => ({ default: {
  name: 'BlackHoleScene', props: ['motion', 'paused'],
  setup(_props: unknown, { expose }: { expose: (value: unknown) => void }) { expose({ setDeepening, replay }) },
  template: '<div data-testid="black-hole-scene" />'
} }))
const wrappers: VueWrapper[] = []
function mountHome(settings: Record<string, unknown> = {}) {
  appStore.cachedPublicSettings = { site_name: 'Test site', site_subtitle: 'Test subtitle', ...settings }
  const view = mount(HomeView, { attachTo: document.body, global: { stubs: {
    RouterLink: RouterLinkStub, LocaleSwitcher: { template: '<div data-testid="locale-switcher" />' }, Icon: { template: '<span data-testid="icon" />' }
  } } })
  wrappers.push(view)
  return view
}
function destination(view: ReturnType<typeof mountHome>) { return view.findComponent(RouterLinkStub).props('to') }
function plaza(view: ReturnType<typeof mountHome>) { return view.findAllComponents(RouterLinkStub).find(link => link.props('to') === '/model-plaza') }

beforeEach(() => {
  authStore.isAuthenticated = authStore.isAdmin = false
  authStore.user = null
  appStore.publicSettingsLoaded = true
  vi.clearAllMocks()
  localStorage.clear()
  vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
  initializeAppearance({ theme: 'light', allow_user_override: true })
})
afterEach(() => { wrappers.splice(0).forEach(view => view.unmount()); document.body.innerHTML = '' })

describe('homepage settings and real entry points', () => {
  it('renders custom HTML ahead of both built-in homes without starting the renderer', () => {
    const view = mountHome({ compact_home_enabled: true, home_content: '<section id="custom-home">Custom home</section>' })
    expect(view.get('#custom-home').text()).toBe('Custom home')
    expect(view.find('[data-testid="compact-home"]').exists()).toBe(false)
    expect(view.find('[data-testid="black-hole-scene"]').exists()).toBe(false)
  })
  it('preserves custom URL mode and iframe title', () => {
    const view = mountHome({ compact_home_enabled: true, home_content: ' https://example.com/home ' })
    expect(view.get('iframe').attributes()).toMatchObject({ src: 'https://example.com/home', title: 'Test site' })
    expect(view.find('[data-testid="black-hole-scene"]').exists()).toBe(false)
  })
  it('uses the lightweight compact home for whitespace-only custom content', () => {
    const view = mountHome({ compact_home_enabled: true, home_content: ' \n\t ' })
    expect(view.get('[data-testid="compact-home"]').text()).toContain('Test site')
    expect(view.find('[data-testid="black-hole-scene"]').exists()).toBe(false)
  })
  it.each([undefined, false])('renders the black-hole default when compact is %s', compact => {
    const view = mountHome(compact === undefined ? {} : { compact_home_enabled: compact })
    expect(view.find('[data-testid="default-home"]').exists()).toBe(true)
    expect(view.find('[data-testid="black-hole-scene"]').exists()).toBe(true)
  })
  it.each([true, false])('links anonymous visitors to login (compact=%s)', compact => {
    expect(destination(mountHome({ compact_home_enabled: compact }))).toBe('/login')
  })
  it.each([true, false])('links regular users to their dashboard (compact=%s)', compact => {
    authStore.isAuthenticated = true
    expect(destination(mountHome({ compact_home_enabled: compact }))).toBe('/dashboard')
  })
  it.each([true, false])('links administrators to the admin dashboard (compact=%s)', compact => {
    authStore.isAuthenticated = authStore.isAdmin = true
    expect(destination(mountHome({ compact_home_enabled: compact }))).toBe('/admin/dashboard')
    expect(authStore.checkAuth).toHaveBeenCalledOnce()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })
  it('preserves custom branding and document links', () => {
    const view = mountHome({ site_name: 'A different brand', site_subtitle: 'Custom subtitle', site_logo: 'https://example.com/custom.svg', doc_url: 'https://example.com/docs' })
    expect(view.text()).toContain('A different brand')
    expect(view.text()).toContain('Custom subtitle')
    expect(view.get('.home-logo').attributes('src')).toBe('https://example.com/custom.svg')
    expect(view.get('.home-doc').attributes('href')).toBe('https://example.com/docs')
  })
  it('rejects unsafe branding and document URLs', () => {
    const view = mountHome({ site_logo: 'javascript:alert(1)', doc_url: 'javascript:alert(1)' })
    expect(view.get('.home-logo').attributes('src')).toBe('/logo.svg')
    expect(view.find('.home-doc').exists()).toBe(false)
  })
  it.each([true, false])('keeps model plaza feature gates (compact=%s)', compact => {
    const publicEntry = plaza(mountHome({ compact_home_enabled: compact, model_plaza_enabled: true, model_plaza_require_auth: false }))
    expect(publicEntry).toBeDefined()
    expect(publicEntry?.attributes('aria-label')).toBe('nav.modelPlaza')
    expect(publicEntry?.find('[data-testid="icon"]').exists()).toBe(true)
    expect(plaza(mountHome({ compact_home_enabled: compact, model_plaza_enabled: true, model_plaza_require_auth: true }))).toBeUndefined()
    authStore.isAuthenticated = true
    expect(plaza(mountHome({ compact_home_enabled: compact, model_plaza_enabled: true, model_plaza_require_auth: true }))).toBeDefined()
    expect(plaza(mountHome({ compact_home_enabled: compact, model_plaza_enabled: false }))).toBeUndefined()
  })
  it('contains no prototype labels or rendering-origin commentary', () => {
    expect(mountHome().text()).not.toMatch(/reference-inspired|本地重制|概念预览|研究编号|NEXUS|Kerrfield/i)
  })
})

describe('homepage motion and keyboard lifecycle', () => {
  it('keeps the global color mode unchanged for the cinematic default', () => {
    mountHome()
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
  it('uses shared appearance for compact theme switching', async () => {
    const view = mountHome({ compact_home_enabled: true })
    await view.get('button[title="home.switchToDark"]').trigger('click')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(view.find('button[title="home.switchToLight"]').exists()).toBe(true)
  })
  it('honors administrator locking for compact appearance', () => {
    configureAppearance({ theme: 'dark', allow_user_override: false })
    expect(mountHome({ compact_home_enabled: true }).find('button[title="home.switchToLight"]').exists()).toBe(false)
  })
  it('never resumes disabled motion through a local toggle', () => {
    configureAppearance({ motion: 'off', allow_user_override: false })
    const view = mountHome()
    expect(view.find('[data-testid="home-pause"]').exists()).toBe(false)
    expect(view.find('[data-testid="home-deepen"]').exists()).toBe(false)
    expect(view.findComponent({ name: 'BlackHoleScene' }).props('motion')).toBe('off')
  })
  it('removes deepening for moderate motion', () => {
    configureAppearance({ motion: 'moderate' })
    expect(mountHome().find('[data-testid="home-deepen"]').exists()).toBe(false)
  })
  it('pauses via the real scene prop and disables deepening', async () => {
    const view = mountHome()
    await view.get('[data-testid="home-pause"]').trigger('click')
    expect(view.findComponent({ name: 'BlackHoleScene' }).props('paused')).toBe(true)
    expect(view.find('[data-testid="home-deepen"]').exists()).toBe(false)
  })
  it('keeps pure mode accessible and restores trigger focus on Escape', async () => {
    const view = mountHome()
    await view.get('[data-testid="home-pure"]').trigger('click')
    expect(view.get('.home-stage').classes()).toContain('is-pure')
    expect(document.activeElement).toBe(view.get('.home-stage').element)
    await view.get('.home-stage').trigger('keydown', { code: 'Space', key: ' ' })
    expect(setDeepening).toHaveBeenLastCalledWith(true)
    await view.get('.home-stage').trigger('keyup', { code: 'Space', key: ' ' })
    expect(setDeepening).toHaveBeenLastCalledWith(false)
    await view.get('.home-stage').trigger('keydown', { key: 'Escape' })
    expect(view.get('.home-stage').classes()).not.toContain('is-pure')
    expect(document.activeElement).toBe(view.get('[data-testid="home-pure"]').element)
  })
  it('does not hijack Space on unrelated inputs or after unmount', async () => {
    const view = mountHome()
    setDeepening.mockClear()
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.dispatchEvent(new KeyboardEvent('keydown', { code: 'Space', bubbles: true }))
    expect(setDeepening).not.toHaveBeenCalledWith(true)
    view.unmount()
    setDeepening.mockClear()
    window.dispatchEvent(new KeyboardEvent('keydown', { code: 'Space', bubbles: true }))
    expect(setDeepening).not.toHaveBeenCalled()
    input.remove()
  })
})
