<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from './BaseDialog.vue'
import AppearanceFields from './AppearanceFields.vue'
import { useAppearance } from '@/composables/useAppearance'
import type { AppearanceSettings } from '@/types/appearance'

withDefaults(defineProps<{ collapsed?: boolean }>(), { collapsed: false })
const { t } = useI18n()
const open = ref(false)
const { effective, allowUserOverride, setPreferences, resetPreferences } = useAppearance()
function update(value: AppearanceSettings) {
  setPreferences({ theme: value.theme, accent: value.accent, motion: value.motion, density: value.density })
}
</script>

<template>
  <button type="button" class="sidebar-link w-full" :title="t('common.appearance.title')" :aria-label="t('common.appearance.title')" data-testid="appearance-preferences" @click="open = true">
    <svg class="h-5 w-5 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="4" /><path d="M3 12h18M12 12v9" /><circle cx="8" cy="7.5" r="1" fill="currentColor" /></svg>
    <span v-if="!collapsed">{{ t('common.appearance.title') }}</span>
  </button>
  <BaseDialog :show="open" :title="t('common.appearance.title')" @close="open = false">
    <p class="mb-5 text-sm text-gray-600 dark:text-dark-300">{{ t('common.appearance.personalHint') }}</p>
    <p v-if="!allowUserOverride" class="mb-4 rounded-lg border-2 p-3 text-sm" role="status">{{ t('common.appearance.locked') }}</p>
    <AppearanceFields :model-value="effective" :disabled="!allowUserOverride" @update:model-value="update" />
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="!allowUserOverride" @click="resetPreferences">{{ t('common.appearance.reset') }}</button>
      <button type="button" class="btn btn-primary" @click="open = false">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>
