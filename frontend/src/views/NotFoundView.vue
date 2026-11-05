<template>
  <main class="not-found-page">
    <section class="not-found-card" aria-labelledby="not-found-title">
      <div class="not-found-code" aria-hidden="true">404</div>
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
.not-found-page { display: grid; min-height: 100vh; place-items: center; padding: 36px 22px; background: var(--ui-canvas); color: var(--ui-ink); }
.not-found-card { width: 100%; max-width: 540px; overflow: hidden; border: 1px solid var(--ui-border); border-radius: var(--ui-radius); background: var(--ui-surface); box-shadow: var(--ui-shadow); }
.not-found-code { padding: 32px 28px; border-bottom: 1px solid var(--ui-border); background: var(--ui-surface-raised); color: var(--ui-muted); font-size: clamp(96px, 23vw, 150px); font-weight: 500; letter-spacing: -.06em; line-height: 1; }
.not-found-content { padding: 32px 28px; }
.not-found-content h1 { font-size: 28px; font-weight: 600; letter-spacing: -.03em; line-height: 1.3; }
.not-found-content p { margin-top: 14px; font-size: 14px; color: var(--ui-muted); line-height: 1.8; }
.not-found-actions { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 28px; }
@media (max-width: 420px) { .not-found-content { padding: 26px 20px; } .not-found-content h1 { font-size: 24px; } .not-found-actions { flex-direction: column; } }
</style>
