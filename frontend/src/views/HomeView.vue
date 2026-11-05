<template>
  <!-- Administrator HTML/URL always takes precedence over either built-in home. -->
  <div v-if="hasHomeContent" class="min-h-screen">
    <iframe v-if="isHomeContentUrl" :src="homeContent.trim()" :title="siteName" class="h-screen w-full border-0" allowfullscreen></iframe>
    <!-- Trusted administrator-authored content: preserve the existing HTML contract. -->
    <div v-else v-html="homeContent"></div>
  </div>

  <div v-else class="public-home" :class="{ 'public-home--compact': compactHomeEnabled }" :data-testid="compactHomeEnabled ? 'compact-home' : 'default-home'">
    <header class="home-header">
      <nav class="home-nav">
        <div class="home-brand">
          <img :src="siteLogo || '/logo.svg'" alt="Logo" class="home-logo" />
          <span>{{ siteName }}</span>
        </div>
        <div class="home-actions">
          <LocaleSwitcher />
          <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer" class="home-icon-button" :title="t('home.viewDocs')" :aria-label="t('home.viewDocs')">
            <Icon name="book" size="md" />
          </a>
          <router-link v-if="showModelPlazaEntry" to="/model-plaza" class="home-plaza-link" :title="t('nav.modelPlaza')">
            <Icon name="grid" size="md" />
            <span>{{ t('nav.modelPlaza') }}</span>
          </router-link>
          <button v-if="allowUserOverride" type="button" class="home-icon-button" :title="isDark ? t('home.switchToLight') : t('home.switchToDark')" :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')" @click="toggleTheme">
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>
          <router-link :to="isAuthenticated ? dashboardPath : '/login'" class="btn btn-primary home-login">
            {{ isAuthenticated ? t('home.dashboard') : t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <main v-if="compactHomeEnabled" class="home-compact-main">
      <div class="home-compact-content">
        <img :src="siteLogo || '/logo.svg'" alt="Logo" class="home-compact-logo" />
        <h1>{{ siteName }}</h1>
        <p>{{ siteSubtitle }}</p>
        <router-link :to="isAuthenticated ? dashboardPath : '/login'" class="btn btn-primary btn-lg">
          {{ isAuthenticated ? t('home.goToDashboard') : t('home.login') }}
        </router-link>
      </div>
    </main>

    <main v-else class="home-main">
      <section class="home-hero" aria-labelledby="home-title">
        <div class="home-hero-copy">
          <h1 id="home-title">{{ siteName }}</h1>
          <p class="home-subtitle">{{ siteSubtitle }}</p>
          <router-link :to="isAuthenticated ? dashboardPath : '/login'" class="btn btn-primary btn-lg home-start">
            {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
            <Icon name="arrowRight" size="md" />
          </router-link>
          <div class="home-tags">
            <span><Icon name="swap" size="sm" />{{ t('home.tags.subscriptionToApi') }}</span>
            <span><Icon name="shield" size="sm" />{{ t('home.tags.stickySession') }}</span>
            <span><Icon name="chart" size="sm" />{{ t('home.tags.realtimeBilling') }}</span>
          </div>
        </div>

        <!-- A local illustration, never an API call or a claimed operational metric. -->
        <section class="terminal-container request-demo" :aria-label="t('home.demo.title')">
          <div class="demo-header">
            <span class="demo-symbol" aria-hidden="true">{ }</span>
            <h2>{{ t('home.demo.title') }}</h2>
          </div>
          <p class="demo-note">{{ t('home.demo.note') }}</p>
          <div class="demo-request-heading">
            <h3>{{ t('home.demo.request') }}</h3>
            <div class="demo-formats" role="group" :aria-label="t('home.demo.request')">
              <button v-for="format in demoFormats" :key="format" type="button" :aria-pressed="demoFormat === format" @click="demoFormat = format">{{ format }}</button>
            </div>
          </div>
          <pre class="demo-code" tabindex="0"><code>{{ demoRequest }}</code></pre>
          <div class="demo-controls">
            <button type="button" class="btn btn-primary" data-testid="demo-run" @click="runDemo">
              <Icon name="arrowRight" size="sm" />
              {{ demoComplete ? t('home.demo.replay') : t('home.demo.run') }}
            </button>
            <button v-if="demoComplete" type="button" class="btn btn-secondary" @click="demoComplete = false">{{ t('home.demo.reset') }}</button>
          </div>
          <div class="demo-response" aria-live="polite" aria-atomic="true">
            <div class="demo-response-heading"><span class="demo-response-dot" :class="{ 'is-complete': demoComplete }" aria-hidden="true"></span><h3>{{ t('home.demo.response') }}</h3></div>
            <pre v-if="demoComplete" :key="demoRuns" class="demo-result"><code>{{ demoResponse }}</code></pre>
            <p v-else class="demo-ready">{{ t('home.demo.ready') }}</p>
          </div>
        </section>
      </section>

      <section class="home-features">
        <article v-for="feature in features" :key="feature.title" class="home-feature">
          <div class="home-feature-icon"><Icon :name="feature.icon" size="lg" /></div>
          <h2>{{ t(feature.title) }}</h2>
          <p>{{ t(feature.description) }}</p>
        </article>
      </section>

      <section class="home-providers" aria-labelledby="providers-title">
        <div class="home-providers-heading">
          <h2 id="providers-title">{{ t('home.providers.title') }}</h2>
          <p>{{ t('home.providers.description') }}</p>
        </div>
        <div class="home-provider-list">
          <div v-for="provider in providers" :key="provider.name" class="home-provider">
            <span class="provider-initial" aria-hidden="true">{{ provider.initial }}</span>
            <span>{{ provider.name }}</span>
            <span class="provider-status">{{ t('home.providers.supported') }}</span>
          </div>
          <div class="home-provider home-provider--soon"><span class="provider-initial" aria-hidden="true">+</span><span>{{ t('home.providers.more') }}</span><span class="provider-status">{{ t('home.providers.soon') }}</span></div>
        </div>
      </section>
    </main>

    <footer class="home-footer">
      <div><p>&copy; {{ currentYear }} {{ siteName }}<template v-if="!compactHomeEnabled">. {{ t('home.footer.allRightsReserved') }}</template></p>
        <div v-if="!compactHomeEnabled" class="home-footer-links">
          <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('home.docs') }}</a>
          <a :href="githubUrl" target="_blank" rel="noopener noreferrer">GitHub</a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import { sanitizeUrl } from '@/utils/url'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'
import { useAppearance } from '@/composables/useAppearance'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const { isDark, toggleTheme, allowUserOverride } = useAppearance()
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform')
const docUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''))
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')
const hasHomeContent = computed(() => homeContent.value.trim().length > 0)
const compactHomeEnabled = computed(() => appStore.cachedPublicSettings?.compact_home_enabled === true)
const modelPlazaEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.modelPlaza))
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})
const githubUrl = 'https://github.com/Wei-Shaw/sub2api'
const isAuthenticated = computed(() => authStore.isAuthenticated)
const modelPlazaRequiresAuth = computed(() => appStore.cachedPublicSettings?.model_plaza_require_auth === true)
const showModelPlazaEntry = computed(() => modelPlazaEnabled.value && (isAuthenticated.value || !modelPlazaRequiresAuth.value))
const dashboardPath = computed(() => authStore.isAdmin ? '/admin/dashboard' : '/dashboard')
const currentYear = computed(() => new Date().getFullYear())

const features = [
  { icon: 'server' as const, title: 'home.features.unifiedGateway', description: 'home.features.unifiedGatewayDesc' },
  { icon: 'users' as const, title: 'home.features.multiAccount', description: 'home.features.multiAccountDesc' },
  { icon: 'creditCard' as const, title: 'home.features.balanceQuota', description: 'home.features.balanceQuotaDesc' }
]
const providers = computed(() => [
  { name: t('home.providers.claude'), initial: 'C' },
  { name: 'GPT', initial: 'G' },
  { name: t('home.providers.gemini'), initial: 'G' },
  { name: t('home.providers.antigravity'), initial: 'A' }
])
const demoFormats = ['cURL', 'JSON'] as const
const demoFormat = ref<typeof demoFormats[number]>('cURL')
const demoComplete = ref(false)
const demoRuns = ref(0)
const demoBody = computed(() => JSON.stringify({ model: 'your-model', max_tokens: 64, messages: [{ role: 'user', content: t('home.demo.prompt') }] }, null, 2))
const demoRequest = computed(() => demoFormat.value === 'JSON' ? demoBody.value : `curl /v1/messages \\\n  -H "x-api-key: YOUR_API_KEY" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "content-type: application/json" \\\n  -d '${demoBody.value}'`)
const demoResponse = computed(() => JSON.stringify({ content: [{ type: 'text', text: t('home.demo.reply') }] }, null, 2))
function runDemo() {
  demoRuns.value += 1
  demoComplete.value = true
}
onMounted(() => {
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) appStore.fetchPublicSettings()
})
</script>

<style scoped>
.public-home { display: flex; min-height: 100vh; flex-direction: column; background: var(--neo-canvas); color: var(--neo-ink); }
.home-header { position: relative; z-index: 20; background: var(--neo-surface); border-bottom: 2px solid var(--neo-border); }
.home-nav { display: flex; max-width: 1200px; margin: auto; align-items: center; justify-content: space-between; gap: 24px; padding: 18px 28px; }
.home-brand { display: flex; align-items: center; gap: 12px; min-width: 0; flex: 1; font-size: 20px; font-weight: 900; letter-spacing: -.04em; }
.home-brand span { overflow-wrap: anywhere; }
.home-logo { width: 42px; height: 42px; flex: 0 0 42px; object-fit: contain; padding: 4px; background: #fff; border: 2px solid var(--neo-border); border-radius: 9px; box-shadow: var(--neo-shadow-sm); }
.home-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 10px; }
.home-icon-button, .home-plaza-link { display: inline-flex; min-height: 40px; align-items: center; justify-content: center; gap: 7px; padding: 8px; border: 2px solid transparent; border-radius: 8px; font-size: 13px; font-weight: 750; }
.home-icon-button:hover, .home-plaza-link:hover { border-color: var(--neo-border); background: var(--neo-surface-soft); }
.home-login { white-space: normal; text-align: center; }
.home-main { width: 100%; max-width: 1200px; margin: auto; padding: 76px 28px 64px; }
.home-hero { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr); align-items: center; gap: 56px; margin-bottom: 64px; }
.home-hero-copy { min-width: 0; }
.home-hero h1, .home-compact-content h1 { font-size: clamp(38px, 5.8vw, 78px); font-weight: 950; letter-spacing: -.065em; line-height: 1.12; overflow-wrap: anywhere; }
.home-subtitle { max-width: 32em; margin: 24px 0 32px; font-size: 19px; line-height: 1.8; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--neo-muted); }
.home-start { max-width: 100%; }
.home-tags { display: flex; flex-wrap: wrap; gap: 12px 18px; margin-top: 30px; }
.home-tags span { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; font-weight: 700; }
.request-demo { min-width: 0; border: 2px solid var(--neo-border); border-radius: 14px; box-shadow: 8px 8px 0 var(--neo-shadow-color); background: var(--neo-surface); overflow: hidden; }
.demo-header { display: flex; align-items: center; gap: 12px; padding: 16px 22px; background: var(--neo-blue); color: #111; border-bottom: 2px solid var(--neo-border); }
.demo-header h2 { font-size: 16px; font-weight: 900; }
.demo-symbol { display: grid; place-items: center; width: 34px; height: 30px; font-weight: 900; border: 2px solid #111; border-radius: 5px; background: #fff; box-shadow: 2px 2px 0 #111; }
.demo-note { padding: 14px 22px 0; font-size: 12px; line-height: 1.65; color: var(--neo-muted); }
.demo-request-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 15px 22px 10px; }
.demo-request-heading h3, .demo-response h3 { font-size: 12px; font-weight: 800; }
.demo-formats { display: flex; border: 2px solid var(--neo-border); border-radius: 6px; overflow: hidden; }
.demo-formats button { padding: 4px 10px; font-size: 11px; font-weight: 800; }
.demo-formats button + button { border-left: 2px solid var(--neo-border); }
.demo-formats button[aria-pressed='true'] { background: var(--neo-accent); color: var(--neo-accent-ink); }
.demo-code { margin: 0 22px; padding: 16px; height: 242px; overflow: auto; background: var(--neo-surface-soft); border: 1px solid var(--neo-divider); border-left: 4px solid var(--neo-border); border-radius: 4px; font-size: 11px; line-height: 1.8; tab-size: 2; }
.demo-controls { display: flex; flex-wrap: wrap; gap: 12px; padding: 18px 22px; }
.demo-controls .btn { font-size: 12px; }
.demo-response { min-height: 152px; padding: 16px 22px; border-top: 2px solid var(--neo-border); }
.demo-response-heading { display: flex; align-items: center; gap: 7px; margin-bottom: 10px; }
.demo-response-dot { width: 10px; height: 10px; border: 2px solid var(--neo-border); border-radius: 3px; background: var(--neo-surface); }
.demo-response-dot.is-complete { background: var(--neo-pink); }
.demo-ready { font-size: 12px; color: var(--neo-muted); padding-top: 12px; }
.demo-result { overflow: auto; font-size: 11px; line-height: 1.7; }
.home-features { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 22px; margin-bottom: 60px; }
.home-feature { min-width: 0; padding: 26px; border: 2px solid var(--neo-border); border-radius: 12px; background: var(--neo-surface); box-shadow: var(--neo-shadow); }
.home-feature-icon { display: inline-flex; padding: 10px; border: 2px solid var(--neo-border); border-radius: 8px; background: var(--neo-blue); color: #111; box-shadow: 3px 3px 0 var(--neo-shadow-color); margin-bottom: 22px; }
.home-feature:nth-child(2) .home-feature-icon { background: var(--neo-pink); }
.home-feature:nth-child(3) .home-feature-icon { background: var(--neo-accent); color: var(--neo-accent-ink); }
.home-feature h2 { font-size: 19px; font-weight: 900; margin-bottom: 10px; }
.home-feature p { font-size: 13px; line-height: 1.9; color: var(--neo-muted); }
.home-providers { border-top: 2px solid var(--neo-border); padding-top: 32px; }
.home-providers-heading h2 { font-size: 26px; font-weight: 900; letter-spacing: -.04em; }
.home-providers-heading p { font-size: 13px; margin: 8px 0 22px; color: var(--neo-muted); }
.home-provider-list { display: flex; flex-wrap: wrap; gap: 12px; }
.home-provider { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 12px; border: 2px solid var(--neo-border); border-radius: 8px; background: var(--neo-surface); font-size: 13px; font-weight: 800; }
.provider-initial { width: 28px; height: 28px; display: grid; place-items: center; border: 2px solid var(--neo-border); border-radius: 5px; background: var(--neo-surface-soft); }
.provider-status { font-size: 10px; font-weight: 650; color: var(--neo-muted); }
.home-provider--soon { border-style: dashed; }
.home-footer { margin-top: auto; border-top: 2px solid var(--neo-border); padding: 24px 28px; background: var(--neo-surface); color: var(--neo-muted); font-size: 12px; overflow-wrap: anywhere; }
.home-footer > div { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 20px; max-width: 1144px; margin: auto; }
.home-footer-links { display: flex; gap: 20px; }
.home-footer a { text-decoration: underline; text-underline-offset: 4px; }
.home-compact-main { display: flex; flex: 1; align-items: center; justify-content: center; padding: 64px 24px; }
.home-compact-content { width: 100%; max-width: 680px; text-align: center; }
.home-compact-logo { width: 88px; height: 88px; object-fit: contain; margin: 0 auto 28px; padding: 10px; background: #fff; border: 2px solid var(--neo-border); border-radius: 15px; box-shadow: 5px 5px 0 var(--neo-shadow-color); }
.home-compact-content h1 { font-size: clamp(32px, 5vw, 52px); }
.home-compact-content p { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.8; color: var(--neo-muted); margin: 20px 0 28px; }
.public-home--compact .home-footer > div { justify-content: center; text-align: center; }
@keyframes home-print { from { opacity: 0; transform: translate(8px, 8px); box-shadow: 0 0 0 var(--neo-shadow-color); } to { opacity: 1; transform: none; } }
@keyframes demo-arrive { from { opacity: 0; clip-path: inset(0 0 100% 0); } to { opacity: 1; clip-path: inset(0); } }
html[data-motion='rich'] .request-demo { animation: home-print 650ms cubic-bezier(.2,.8,.2,1) both; }
html[data-motion='rich'] .demo-result { animation: demo-arrive 450ms ease-out both; }
html[data-motion='moderate'] .demo-result { animation: none; }
@media (prefers-reduced-motion: reduce) { .request-demo, .demo-result { animation: none !important; } }
@media (max-width: 1000px) { .home-hero { gap: 28px; } .home-nav { gap: 16px; flex-wrap: wrap; } .home-brand { flex-basis: 200px; } .home-main { padding-top: 48px; } }
@media (max-width: 767px) { .home-nav { padding: 16px; } .home-brand { flex-basis: 100%; font-size: 18px; } .home-actions { width: 100%; justify-content: flex-start; gap: 8px; } .home-login { margin-left: auto; } .home-plaza-link span { display: none; } .home-main { padding: 40px 18px; } .home-hero { grid-template-columns: minmax(0, 1fr); gap: 36px; margin-bottom: 36px; } .home-hero h1 { font-size: clamp(38px, 10vw, 64px); } .home-subtitle { font-size: 16px; margin-top: 18px; } .home-features { grid-template-columns: minmax(0, 1fr); gap: 18px; margin-bottom: 36px; } .home-feature { padding: 22px; } .home-feature-icon { margin-bottom: 16px; } .request-demo { box-shadow: 5px 5px 0 var(--neo-shadow-color); } .demo-header, .demo-response { padding: 16px; } .demo-note { padding: 12px 16px 0; } .demo-request-heading { padding: 14px 16px 10px; } .demo-code { margin: 0 16px; padding: 12px; } .demo-controls { padding: 16px; gap: 10px; } .home-footer { padding: 22px 18px; } }
</style>
