<template>
  <main class="not-found-page">
    <section class="not-found-card" aria-labelledby="not-found-title">
      <div class="not-found-code" aria-hidden="true">404<span>?</span></div>
      <div class="not-found-content">
        <h1 id="not-found-title">{{ t('errors.pageNotFound') }}</h1>
        <p>{{ t('errors.pageNotFoundDescription') }}</p>
        <div class="not-found-actions">
          <button type="button" class="btn btn-secondary" @click="goBack"><Icon name="arrowLeft" size="md" />{{ t('common.back') }}</button>
          <router-link to="/dashboard" class="btn btn-primary"><Icon name="home" size="md" />{{ t('home.goToDashboard') }}</router-link>
        </div>
      </div>
    </section>
  </main>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const router = useRouter()
function goBack(): void { router.back() }
</script>

<style scoped>
.not-found-page { display: grid; min-height: 100vh; place-items: center; padding: 36px 22px; background: var(--neo-canvas); color: var(--neo-ink); }
.not-found-card { width: 100%; max-width: 540px; overflow: hidden; border: 2px solid var(--neo-border); border-radius: 14px; background: var(--neo-surface); box-shadow: 7px 7px 0 var(--neo-shadow-color); }
.not-found-code { position: relative; padding: 32px 28px; border-bottom: 2px solid var(--neo-border); background: var(--neo-accent); color: var(--neo-accent-ink); font-size: clamp(96px, 23vw, 150px); font-weight: 950; letter-spacing: -.08em; line-height: 1; }
.not-found-code span { position: absolute; right: 28px; bottom: 30px; display: grid; place-items: center; width: 48px; height: 48px; border: 2px solid #111; border-radius: 9px; background: var(--neo-pink); color: #111; font-size: 30px; letter-spacing: 0; box-shadow: 3px 3px 0 #111; }
.not-found-content { padding: 32px 28px; }
.not-found-content h1 { font-size: 28px; font-weight: 900; letter-spacing: -.04em; line-height: 1.3; }
.not-found-content p { margin-top: 14px; font-size: 14px; color: var(--neo-muted); line-height: 1.8; }
.not-found-actions { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 28px; }
@media (max-width: 420px) { .not-found-content { padding: 26px 20px; } .not-found-content h1 { font-size: 24px; } .not-found-actions { flex-direction: column; } }
</style>
