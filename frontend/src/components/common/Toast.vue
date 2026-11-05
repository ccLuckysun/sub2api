<template>
  <Teleport to="body">
    <div
      class="pointer-events-none fixed right-4 top-4 z-[9999] w-[calc(100vw-32px)] max-w-md space-y-3"
      aria-live="polite"
      aria-atomic="true"
    >
      <TransitionGroup
        enter-active-class="transition ease-out duration-300"
        enter-from-class="opacity-0 translate-x-full"
        enter-to-class="opacity-100 translate-x-0"
        leave-active-class="transition ease-in duration-200"
        leave-from-class="opacity-100 translate-x-0"
        leave-to-class="opacity-0 translate-x-full"
      >
        <div
          v-for="toast in toasts"
          :key="toast.id"
          :class="[
            'neo-toast pointer-events-auto w-full overflow-hidden',
            getBorderColor(toast.type)
          ]"
        >
          <div class="p-4">
            <div class="flex items-start gap-3">
              <!-- Icon -->
              <div class="mt-0.5 flex-shrink-0">
                <Icon
                  :name="getToastIconName(toast.type)"
                  size="md"
                  :class="getIconColor(toast.type)"
                  aria-hidden="true"
                />
              </div>

              <!-- Content -->
              <div class="min-w-0 flex-1">
                <p v-if="toast.title" class="text-sm font-semibold text-gray-900 dark:text-dark-100">
                  {{ toast.title }}
                </p>
                <p
                  :class="[
                    'text-sm leading-relaxed',
                    toast.title
                      ? 'mt-1 text-gray-600 dark:text-gray-300'
                      : 'text-gray-900 dark:text-dark-100'
                  ]"
                >
                  {{ toast.message }}
                </p>
              </div>

              <!-- Close button -->
              <button
                @click="removeToast(toast.id)"
                class="-m-1 flex-shrink-0 rounded p-1 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:text-gray-500 dark:hover:bg-dark-700 dark:hover:text-gray-300"
                aria-label="Close notification"
              >
                <Icon name="x" size="sm" />
              </button>
            </div>
          </div>

          <!-- Progress bar -->
          <p v-if="toast.duration && motion === 'off'" class="px-4 pb-3 text-xs text-gray-600 dark:text-gray-300" aria-hidden="true">
            {{ t('common.appearance.remaining', { seconds: remainingSeconds(toast) }) }}
          </p>
          <div v-else-if="toast.duration" class="h-1 bg-gray-100 dark:bg-dark-700">
            <div
              :class="['h-full toast-progress', getProgressBarColor(toast.type)]"
              :style="{ animationDuration: `${toast.duration}ms` }"
            ></div>
          </div>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppearance } from '@/composables/useAppearance'
import type { Toast } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'

const appStore = useAppStore()

const toasts = computed(() => appStore.toasts)
const { motion } = useAppearance()
const { t } = useI18n()
const now = ref(Date.now())
let countdown: ReturnType<typeof setInterval> | undefined
function stopCountdown() {
  if (countdown !== undefined) clearInterval(countdown)
  countdown = undefined
}
watch(() => motion.value === 'off' && toasts.value.some(toast => toast.duration), (enabled) => {
  stopCountdown()
  if (enabled) {
    now.value = Date.now()
    countdown = setInterval(() => { now.value = Date.now() }, 250)
  }
}, { immediate: true })
onBeforeUnmount(stopCountdown)
function remainingSeconds(toast: Toast): number {
  return Math.max(0, Math.ceil(((toast.startTime ?? now.value) + (toast.duration ?? 0) - now.value) / 1000))
}

const getToastIconName = (type: string): 'checkCircle' | 'xCircle' | 'exclamationTriangle' | 'infoCircle' => {
  switch (type) {
    case 'success':
      return 'checkCircle'
    case 'error':
      return 'xCircle'
    case 'warning':
      return 'exclamationTriangle'
    case 'info':
    default:
      return 'infoCircle'
  }
}

const getIconColor = (type: string): string => {
  const colors: Record<string, string> = {
    success: 'text-green-500',
    error: 'text-red-500',
    warning: 'text-yellow-500',
    info: 'text-blue-500'
  }
  return colors[type] || colors.info
}

const getBorderColor = (type: string): string => {
  const colors: Record<string, string> = {
    success: 'border-l-emerald-600',
    error: 'border-l-red-600',
    warning: 'border-l-amber-500',
    info: 'border-l-blue-600'
  }
  return colors[type] || colors.info
}

const getProgressBarColor = (type: string): string => {
  const colors: Record<string, string> = {
    success: 'bg-green-500',
    error: 'bg-red-500',
    warning: 'bg-yellow-500',
    info: 'bg-blue-500'
  }
  return colors[type] || colors.info
}

const removeToast = (id: string) => {
  appStore.hideToast(id)
}
</script>

<style scoped>
.neo-toast { border: 1px solid var(--ui-border); border-left-width: 3px; border-radius: 10px; background: var(--ui-surface); color: var(--ui-ink); box-shadow: var(--ui-shadow); }
/* Colour is semantic and independent of the chosen appearance accent. */
.neo-toast.border-l-emerald-600 { border-left-color: #059669; }
.neo-toast.border-l-red-600 { border-left-color: #dc2626; }
.neo-toast.border-l-amber-500 { border-left-color: #f59e0b; }
.neo-toast.border-l-blue-600 { border-left-color: #2563eb; }

.toast-progress {
  width: 100%;
  animation-name: toast-progress-shrink;
  animation-timing-function: linear;
  animation-fill-mode: forwards;
}

@keyframes toast-progress-shrink {
  from {
    width: 100%;
  }
  to {
    width: 0%;
  }
}
</style>
