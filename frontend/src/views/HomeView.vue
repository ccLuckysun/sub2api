<template>
  <div v-if="hasHomeContent" class="min-h-screen">
    <iframe v-if="isHomeContentUrl" :src="homeContent.trim()" :title="siteName" class="h-screen w-full border-0" allowfullscreen />
    <div v-else v-html="homeContent"></div>
  </div>

  <div v-else class="blackhole-home" :class="{ 'is-compact': compactHomeEnabled }" :data-testid="compactHomeEnabled ? 'compact-home' : 'default-home'">
    <section ref="hero" class="home-stage" :class="{ 'is-pure': pure }" :tabindex="compactHomeEnabled ? undefined : 0" :aria-label="compactHomeEnabled ? undefined : t('home.blackhole.sceneLabel')">
      <BlackHoleScene v-if="!compactHomeEnabled" ref="scene" :motion="motion" :paused="paused" />
      <div v-if="!compactHomeEnabled" class="stage-shade" aria-hidden="true"></div>

      <header class="home-header" :inert="pure || undefined">
        <a class="home-brand" href="/home" :aria-label="siteName"><img :src="siteLogo || '/logo.svg'" :alt="siteName" class="home-logo" /><span>{{ siteName }}</span></a>
        <nav class="home-actions" :aria-label="t('common.toggleMenu')">
          <LocaleSwitcher class="home-locale" />
          <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer" class="home-link home-doc">{{ t('home.docs') }}</a>
          <router-link v-if="showModelPlazaEntry" to="/model-plaza" class="home-link home-plaza-link" :aria-label="t('nav.modelPlaza')" :title="t('nav.modelPlaza')"><Icon name="grid" size="sm" /><span>{{ t('nav.modelPlaza') }}</span></router-link>
          <button v-if="compactHomeEnabled && allowUserOverride" type="button" class="home-icon-button" :title="isDark ? t('home.switchToLight') : t('home.switchToDark')" :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')" @click="toggleTheme"><Icon :name="isDark ? 'sun' : 'moon'" size="md" /></button>
          <template v-if="!compactHomeEnabled">
            <button v-if="motion !== 'off'" type="button" class="home-icon-button" :aria-label="paused ? t('home.blackhole.resume') : t('home.blackhole.pause')" :title="paused ? t('home.blackhole.resume') : t('home.blackhole.pause')" :aria-pressed="paused" data-testid="home-pause" @click="paused = !paused"><Icon v-if="paused" name="play" size="sm" /><svg v-else width="12" height="12" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true"><path d="M3 2h2v8H3zm4 0h2v8H7z" /></svg></button>
            <button ref="pureTrigger" type="button" class="home-icon-button pure-trigger" :aria-label="t('home.blackhole.pure')" :title="t('home.blackhole.pure')" data-testid="home-pure" @click="setPure(true)"><svg width="16" height="16" viewBox="0 0 20 20" fill="none" stroke="currentColor" aria-hidden="true"><path d="M7 3H3v4m10-4h4v4M3 13v4h4m10-4v4h-4" /></svg></button>
          </template>
          <router-link :to="destination" class="home-login">{{ isAuthenticated ? t('home.dashboard') : t('home.login') }}<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" aria-hidden="true"><path d="M2 8h11M9 4l4 4-4 4" /></svg></router-link>
        </nav>
      </header>

      <main v-if="compactHomeEnabled" class="home-compact-main">
        <div class="home-compact-content"><img :src="siteLogo || '/logo.svg'" :alt="siteName" class="home-compact-logo" /><h1>{{ siteName }}</h1><p>{{ siteSubtitle }}</p><router-link :to="destination" class="home-primary">{{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}<Icon name="arrowRight" size="sm" /></router-link></div>
      </main>
      <div v-else class="hero-bottom" :inert="pure || undefined">
        <h1><span>{{ t('home.blackhole.headingLine1') }}</span><span>{{ t('home.blackhole.headingLine2') }}</span></h1>
        <div class="hero-side"><p>{{ siteSubtitle }}</p><div class="hero-cta-row"><router-link :to="destination" class="home-primary">{{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}<span class="cta-dot" aria-hidden="true"></span></router-link><button v-if="canDeepen" type="button" class="home-deepen" :aria-pressed="holding" data-testid="home-deepen" @pointerdown="startPointer" @pointerup="stopHolding" @pointercancel="stopHolding" @lostpointercapture="stopHolding" @keydown="onDeepenKeydown" @keyup="onDeepenKeyup" @blur="stopHolding">{{ t('home.blackhole.hold') }}</button></div></div>
      </div>
      <div v-if="!compactHomeEnabled" class="hero-footnote" :inert="pure || undefined"><a href="#home-capabilities">{{ t('home.blackhole.explore') }}<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" aria-hidden="true"><path d="M8 2v11m-4-4 4 4 4-4" /></svg></a><span v-if="canDeepen" class="keyboard-hint">{{ t('home.blackhole.spaceHint') }}</span></div>
      <button v-if="pure" type="button" class="restore-ui" data-testid="home-restore" @click="setPure(false)">{{ t('home.blackhole.restore') }}</button>
    </section>

    <section v-if="!compactHomeEnabled" id="home-capabilities" class="home-capabilities" :aria-label="t('home.blackhole.capabilities')">
      <article v-for="feature in features" :key="feature.title"><Icon :name="feature.icon" size="lg" /><h2>{{ t(feature.title) }}</h2><p>{{ t(feature.description) }}</p></article>
    </section>
    <footer class="home-footer"><span>&copy; {{ currentYear }} {{ siteName }}</span><a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('home.docs') }}</a></footer>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import BlackHoleScene from '@/components/home/BlackHoleScene.vue'
import { sanitizeUrl } from '@/utils/url'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'
import { useAppearance } from '@/composables/useAppearance'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const { isDark, toggleTheme, allowUserOverride, motion } = useAppearance()
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroSubtitle'))
const docUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''))
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')
const hasHomeContent = computed(() => homeContent.value.trim().length > 0)
const compactHomeEnabled = computed(() => appStore.cachedPublicSettings?.compact_home_enabled === true)
const isHomeContentUrl = computed(() => /^https?:\/\//.test(homeContent.value.trim()))
const isAuthenticated = computed(() => authStore.isAuthenticated)
const destination = computed(() => !isAuthenticated.value ? '/login' : authStore.isAdmin ? '/admin/dashboard' : '/dashboard')
const showModelPlazaEntry = computed(() => isFeatureFlagEnabled(FeatureFlags.modelPlaza) && (isAuthenticated.value || appStore.cachedPublicSettings?.model_plaza_require_auth !== true))
const currentYear = new Date().getFullYear()
const features = [
  { icon: 'server' as const, title: 'home.features.unifiedGateway', description: 'home.features.unifiedGatewayDesc' },
  { icon: 'users' as const, title: 'home.features.multiAccount', description: 'home.features.multiAccountDesc' },
  { icon: 'creditCard' as const, title: 'home.features.balanceQuota', description: 'home.features.balanceQuotaDesc' }
]
const hero = ref<HTMLElement | null>(null)
const scene = ref<{ setDeepening: (active: boolean) => void; replay: () => void } | null>(null)
const pureTrigger = ref<HTMLButtonElement | null>(null)
const paused = ref(false)
const pure = ref(false)
const holding = ref(false)
const heroVisible = ref(true)
const canDeepen = computed(() => !hasHomeContent.value && !compactHomeEnabled.value && motion.value === 'rich' && !paused.value)
let observer: IntersectionObserver | undefined
function stopHolding() { holding.value = false; scene.value?.setDeepening(false) }
function startHolding() { if (!canDeepen.value) return; holding.value = true; scene.value?.setDeepening(true) }
function startPointer(event: PointerEvent) {
  if (!canDeepen.value) return
  startHolding()
  const target = event.currentTarget as HTMLElement
  if (typeof target.setPointerCapture === 'function') target.setPointerCapture(event.pointerId)
}
function onDeepenKeydown(event: KeyboardEvent) {
  if (event.code !== 'Space' && event.key !== 'Enter') return
  event.preventDefault()
  if (!event.repeat) startHolding()
}
function onDeepenKeyup(event: KeyboardEvent) {
  if (event.code === 'Space' || event.key === 'Enter') { event.preventDefault(); stopHolding() }
}
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { stopHolding(); if (pure.value) void setPure(false); return }
  if (event.code !== 'Space' || event.repeat || !canDeepen.value || !heroVisible.value) return
  const target = event.target
  if (!(target instanceof HTMLElement) || !hero.value?.contains(target) || target.closest('a,button,input,textarea,select,[contenteditable]')) return
  event.preventDefault()
  startHolding()
}
function onKeyup(event: KeyboardEvent) { if (event.code === 'Space' || event.key === 'Enter') stopHolding() }
function onVisibility() { if (document.hidden) stopHolding() }
async function setPure(value: boolean) {
  pure.value = value
  stopHolding()
  await nextTick()
  if (value) hero.value?.focus({ preventScroll: true })
  else pureTrigger.value?.focus({ preventScroll: true })
}
watch(canDeepen, value => { if (!value) stopHolding() })
watch([hasHomeContent, compactHomeEnabled], () => { pure.value = false; stopHolding() })
onMounted(() => {
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) void appStore.fetchPublicSettings()
  window.addEventListener('keydown', onKeydown)
  window.addEventListener('keyup', onKeyup)
  window.addEventListener('blur', stopHolding)
  document.addEventListener('visibilitychange', onVisibility)
  if (typeof IntersectionObserver !== 'undefined' && hero.value) {
    observer = new IntersectionObserver(entries => { heroVisible.value = entries[0]?.isIntersecting ?? false; if (!heroVisible.value) stopHolding() })
    observer.observe(hero.value)
  }
})
onBeforeUnmount(() => {
  stopHolding()
  observer?.disconnect()
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('keyup', onKeyup)
  window.removeEventListener('blur', stopHolding)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<style scoped>
.blackhole-home{--home-bg:#030304;--home-ink:#ece8e1;--home-muted:#aaa49e;background:var(--home-bg);color:var(--home-ink);min-height:100vh;color-scheme:dark}
.home-stage{position:relative;min-height:720px;height:100svh;overflow:clip;isolation:isolate;outline:none;background:var(--home-bg)}
.home-stage:focus-visible{box-shadow:inset 0 0 0 1px #b9a9ff70}.stage-shade{position:absolute;inset:0;pointer-events:none;z-index:1;background:linear-gradient(0deg,#030304 0%,#030304b0 9%,#03030425 25%,transparent 50%)}
.home-header{position:absolute;inset:0 0 auto;z-index:5;display:flex;justify-content:space-between;align-items:center;gap:28px;padding:27px 42px}
.home-brand{display:flex;align-items:center;gap:11px;min-width:0;max-width:45%;font-size:20px;font-weight:600;letter-spacing:-.7px;color:inherit}.home-brand span{overflow-wrap:anywhere}.home-logo{width:38px;height:38px;object-fit:contain;flex:none}
.home-actions{display:flex;flex-wrap:wrap;align-items:center;justify-content:flex-end;gap:22px;min-width:0}.home-link{font-size:12px;color:#b1aaa3;transition:color .2s}.home-link:hover{color:#f8f2e9}.home-plaza-link{display:inline-flex;align-items:center;gap:6px}.home-icon-button{width:32px;height:36px;display:flex;align-items:center;justify-content:center;border:0;border-radius:6px;background:transparent;color:#b6aea6}.home-icon-button:hover{background:#ece8e10b;color:#fff}.home-login{display:flex;align-items:center;gap:18px;min-height:36px;padding:0 17px;border:1px solid #ece8e145;border-radius:999px;font-size:11px;color:var(--home-ink)}.home-login:hover{border-color:#ece8e190;background:#ece8e109}
.hero-bottom{position:absolute;left:42px;right:42px;bottom:110px;z-index:3;display:flex;justify-content:space-between;align-items:flex-end;gap:42px}.hero-bottom h1{margin:0;max-width:850px;font-size:clamp(40px,4.8vw,75px);font-weight:500;letter-spacing:-3px;line-height:1.15}.hero-bottom h1 span{display:block}.hero-side{max-width:335px;flex:0 0 335px;display:flex;flex-direction:column;gap:26px;padding-bottom:4px}.hero-side>p{font-size:13px;line-height:1.9;color:var(--home-muted);white-space:pre-wrap;overflow-wrap:anywhere;margin:0}.hero-cta-row{display:flex;align-items:center;flex-wrap:wrap;gap:22px}.home-primary{display:inline-flex;align-items:center;justify-content:center;gap:19px;min-height:44px;padding:0 21px;border:1px solid transparent;border-radius:999px;background:#ece8e1;color:#100f13;font-size:12px;font-weight:500;text-align:center;max-width:100%}.home-primary:hover{background:#d9d0ec}.cta-dot{width:5px;height:5px;flex:none;border-radius:50%;background:#514558}.home-deepen{border:0;background:none;color:#c2bbb3;padding:10px 0;font-size:11px;touch-action:none;user-select:none}.home-deepen:hover,.home-deepen[aria-pressed=true]{color:#dfd1ff}.hero-footnote{position:absolute;left:42px;right:42px;bottom:32px;z-index:3;display:flex;justify-content:space-between;font-size:10px;color:#99928b}.hero-footnote a{display:flex;align-items:center;gap:10px}.keyboard-hint{font-size:9px;color:#827a73}.restore-ui{position:absolute;z-index:8;right:24px;bottom:24px;border:1px solid #ece8e140;border-radius:99px;background:#0b0b0fcf;color:#d0c8bf;padding:10px 15px;font-size:11px}.is-pure :is(.home-header,.hero-bottom,.hero-footnote){opacity:0;pointer-events:none}.home-header,.hero-bottom,.hero-footnote{transition:opacity .3s}
.home-capabilities{max-width:1440px;margin:auto;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:0;border-top:1px solid #ece8e121;padding:60px 42px}.home-capabilities article{min-width:0;padding:0 36px}.home-capabilities article:first-child{padding-left:0}.home-capabilities article+article{border-left:1px solid #ece8e11f}.home-capabilities article:last-child{padding-right:0}.home-capabilities :deep(svg){color:#c6b7a9;margin-bottom:25px}.home-capabilities h2{font-weight:500;font-size:17px;margin:0 0 14px}.home-capabilities p{margin:0;color:#989087;font-size:12px;line-height:1.9}.home-footer{display:flex;justify-content:space-between;gap:20px;flex-wrap:wrap;padding:26px 42px;border-top:1px solid #ece8e11c;color:#8f877f;font-size:10px;overflow-wrap:anywhere}.home-footer a:hover{color:#d7cfc5}
.home-stage :is(a,button):focus-visible{outline:1px solid #c5b9ee;outline-offset:5px}.blackhole-home:not(.is-compact) :deep(.home-locale > button){color:#c9c1b8;background:transparent}.blackhole-home:not(.is-compact) :deep(.home-locale > button:hover){background:#ece8e10b}.blackhole-home:not(.is-compact) :deep(.home-locale > button svg){color:#b8afa8}.is-compact{--home-bg:var(--ui-canvas,var(--neo-canvas));--home-ink:var(--ui-ink,var(--neo-ink));--home-muted:var(--ui-muted,var(--neo-muted));color-scheme:inherit}.is-compact .home-stage{height:auto;min-height:calc(100svh - 68px);display:flex;flex-direction:column}.is-compact .home-header{position:relative;border-bottom:1px solid var(--ui-border,var(--neo-divider));padding-block:20px}.is-compact .home-link,.is-compact .home-icon-button{color:var(--home-muted)}.is-compact .home-login{color:var(--home-ink);border-color:var(--ui-border,var(--neo-border))}.home-compact-main{display:flex;flex:1;align-items:center;justify-content:center;padding:64px 24px}.home-compact-content{text-align:center;min-width:0;max-width:680px}.home-compact-logo{width:80px;height:80px;object-fit:contain;margin:0 auto 27px}.home-compact-content h1{font-size:clamp(32px,5vw,52px);font-weight:500;letter-spacing:-1.8px;margin:0;overflow-wrap:anywhere}.home-compact-content p{white-space:pre-wrap;overflow-wrap:anywhere;font-size:16px;color:var(--home-muted);line-height:1.8;margin:20px 0 29px}.is-compact .home-primary{background:var(--ui-accent,var(--neo-accent));color:var(--ui-accent-ink,var(--neo-accent-ink))}.is-compact .home-footer{border-color:var(--ui-border,var(--neo-divider));color:var(--home-muted);justify-content:center}
@media(max-width:1120px){.hero-bottom h1{font-size:53px;letter-spacing:-2.5px}.hero-side{flex-basis:285px}.home-actions{gap:15px}.hero-bottom{gap:27px}}
@media(max-width:809px){.home-stage{min-height:800px}.home-header{padding:22px 24px;gap:16px;align-items:flex-start}.home-brand{font-size:18px;max-width:48%;gap:7px}.home-logo{width:32px;height:32px}.home-actions{gap:7px}.home-doc{display:none}.home-plaza-link{width:28px;min-height:34px;justify-content:center}.home-plaza-link span{display:none}.home-login{padding:0 12px;gap:9px;font-size:10px;min-height:34px}.home-icon-button{width:27px;height:34px}.hero-bottom{left:24px;right:24px;bottom:91px;flex-direction:column;align-items:flex-start;gap:22px}.hero-bottom h1{font-size:43px;letter-spacing:-2px}.hero-side{flex-basis:auto;gap:20px;max-width:400px}.hero-side>p{font-size:12px;line-height:1.8}.hero-cta-row{gap:21px}.home-primary{min-height:42px;font-size:11px;padding:0 18px}.home-deepen{font-size:10px}.hero-footnote{left:24px;right:24px;bottom:29px}.keyboard-hint{display:none}.home-capabilities{padding:40px 24px;grid-template-columns:1fr;gap:27px}.home-capabilities article,.home-capabilities article:first-child,.home-capabilities article:last-child{padding:0}.home-capabilities article+article{border-left:0;border-top:1px solid #ece8e11f;padding-top:27px}.home-capabilities :deep(svg){margin-bottom:15px}.home-footer{padding:24px}.is-compact .home-header{flex-wrap:wrap}.is-compact .home-brand{max-width:100%}.is-compact .home-actions{flex:1}.is-compact .home-doc{display:initial}.home-compact-content p{font-size:14px}}
@media(max-width:380px){.home-header{padding:19px 18px;gap:9px}.home-brand{font-size:16px;max-width:44%}.home-logo{width:29px;height:29px}.home-actions{gap:2px}.home-login{padding:0 9px;gap:7px;font-size:9px}.home-icon-button{width:24px}.hero-bottom{left:18px;right:18px}.hero-bottom h1{font-size:37px}.hero-footnote{left:18px;right:18px}.home-capabilities,.home-footer{padding-inline:18px}}
@media(prefers-reduced-motion:reduce){.home-header,.hero-bottom,.hero-footnote{transition:none}}
</style>
