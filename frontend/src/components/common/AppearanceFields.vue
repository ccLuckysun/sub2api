<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AppearanceSettings } from '@/types/appearance'

const props = withDefaults(defineProps<{
  modelValue: AppearanceSettings
  admin?: boolean
  disabled?: boolean
}>(), { admin: false, disabled: false })
const emit = defineEmits<{ 'update:modelValue': [value: AppearanceSettings] }>()
const { t } = useI18n()
const choices = {
  theme: ['system', 'light', 'dark'],
  accent: ['yellow', 'pink', 'blue'],
  motion: ['rich', 'moderate', 'off'],
  density: ['standard', 'compact']
} as const
const fields = ['theme', 'accent', 'motion', 'density'] as const

function update(event: Event, key: keyof typeof choices) {
  emit('update:modelValue', { ...props.modelValue, [key]: (event.target as HTMLSelectElement).value })
}
</script>

<template>
  <div class="space-y-5">
    <div class="grid gap-5 sm:grid-cols-2">
      <label v-for="field in fields" :key="field" class="block min-w-0">
        <span class="input-label">{{ t(`common.appearance.${field}`) }}</span>
        <select :data-testid="`appearance-${field}`" class="input w-full" :value="modelValue[field]" :disabled="disabled" @change="update($event, field)">
          <option v-for="option in choices[field]" :key="option" :value="option">{{ t(`common.appearance.options.${option}`) }}</option>
        </select>
      </label>
    </div>
    <label v-if="admin" class="flex cursor-pointer items-start gap-3 rounded-xl border p-4">
      <input type="checkbox" class="mt-1 h-4 w-4 accent-current" data-testid="appearance-allow-override" :checked="modelValue.allow_user_override" :disabled="disabled" @change="emit('update:modelValue', { ...modelValue, allow_user_override: ($event.target as HTMLInputElement).checked })" />
      <span><span class="block font-semibold">{{ t('common.appearance.allowOverride') }}</span><span class="input-hint">{{ t('common.appearance.allowOverrideHint') }}</span></span>
    </label>
    <p class="input-hint">{{ t('common.appearance.motionHint') }}</p>
  </div>
</template>
