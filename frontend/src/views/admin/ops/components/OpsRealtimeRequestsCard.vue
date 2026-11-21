<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useDebounceFn, useIntervalFn, useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  opsAPI,
  type OpsRealtimeRequestItem,
  type OpsRealtimeRequestsResponse,
  type OpsRealtimeRequestSummary
} from '@/api/admin/ops'
import { formatRelativeTime } from '@/utils/format'
import OpsRealtimeRequestDetailModal from './OpsRealtimeRequestDetailModal.vue'
import {
  computePhaseBreakdown,
  formatByteSize,
  formatDurationMs,
  formatObservedDateTimeFull,
  formatObservedTime,
  getFailureReasonLabel,
  getHttpStatusClass,
  isRequestSuccess,
  REALTIME_REQUEST_PHASES
} from '../utils/realtimeRequestFormatters'

interface Props {
  refreshToken?: number
}

const props = withDefaults(defineProps<Props>(), {
  refreshToken: 0
})

const { t } = useI18n()
const isDesktopViewport = useMediaQuery('(min-width: 768px)')

const loading = ref(false)
const hasLoadedOnce = ref(false)
const errorMessage = ref('')
const response = ref<OpsRealtimeRequestsResponse | null>(null)

// Filters
const resultFilter = ref<'all' | 'success' | 'failure'>('all')
const modelFilter = ref<string>('')
const searchInput = ref<string>('')
const appliedSearch = ref<string>('')
const limit = ref<number>(100)

// Auto-refresh (5s)
const autoRefreshEnabled = ref(true)

// Detail modal
const selectedItem = ref<OpsRealtimeRequestItem | null>(null)
const showDetailModal = ref(false)

let activeController: AbortController | null = null
let fetchSeq = 0

function isCanceledRequest(err: unknown): boolean {
  return (
    !!err &&
    typeof err === 'object' &&
    (('code' in err && (err as Record<string, unknown>).code === 'ERR_CANCELED') ||
      ('name' in err && (err as Record<string, unknown>).name === 'CanceledError') ||
      ('name' in err && (err as Record<string, unknown>).name === 'AbortError'))
  )
}

function abortInFlight() {
  if (activeController) {
    activeController.abort()
    activeController = null
  }
}

const isEnabled = computed(() => response.value?.enabled !== false)
const items = computed(() => response.value?.items ?? [])
const summary = computed<OpsRealtimeRequestSummary>(() => {
  return (
    response.value?.summary ?? {
      total: 0,
      success: 0,
      failure: 0,
      success_rate: 0,
      avg_total_ms: 0,
      slow_count: 0
    }
  )
})

const successRateColorClass = computed(() => {
  if (summary.value.total === 0) return 'text-ui-ink'
  const rate = summary.value.success_rate
  if (rate >= 99) return 'text-emerald-600 dark:text-emerald-400'
  if (rate >= 95) return 'text-amber-600 dark:text-amber-400'
  return 'text-red-600 dark:text-red-400'
})

const resultFilterOptions = computed<Array<{ value: 'all' | 'success' | 'failure'; label: string }>>(() => [
  { value: 'all', label: t('admin.ops.realtimeRequests.filters.all') },
  { value: 'success', label: t('admin.ops.realtimeRequests.filters.success') },
  { value: 'failure', label: t('admin.ops.realtimeRequests.filters.failure') }
])

const modelOptions = computed(() => {
  const seen = new Set<string>()
  const opts: Array<{ value: string; label: string }> = [
    { value: '', label: t('admin.ops.realtimeRequests.filters.allModels') }
  ]
  for (const m of response.value?.models ?? []) {
    const trimmed = String(m || '').trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    opts.push({ value: trimmed, label: trimmed })
  }
  if (modelFilter.value && !seen.has(modelFilter.value)) {
    opts.push({ value: modelFilter.value, label: modelFilter.value })
  }
  return opts
})

const limitOptions = computed(() => [
  { value: 50, label: t('admin.ops.realtimeRequests.filters.limitOption', { n: 50 }) },
  { value: 100, label: t('admin.ops.realtimeRequests.filters.limitOption', { n: 100 }) },
  { value: 200, label: t('admin.ops.realtimeRequests.filters.limitOption', { n: 200 }) }
])

async function fetchRealtimeRequests(options: { silent?: boolean } = {}) {
  abortInFlight()
  fetchSeq += 1
  const currentSeq = fetchSeq
  const controller = new AbortController()
  activeController = controller

  if (!options.silent || !hasLoadedOnce.value) {
    loading.value = true
  }
  errorMessage.value = ''

  try {
    const params: {
      limit: number
      result: 'all' | 'success' | 'failure'
      model?: string
      search?: string
    } = {
      limit: limit.value,
      result: resultFilter.value
    }
    if (modelFilter.value.trim()) {
      params.model = modelFilter.value.trim()
    }
    if (appliedSearch.value.trim()) {
      params.search = appliedSearch.value.trim()
    }

    const res = await opsAPI.getRealtimeRequests(params, { signal: controller.signal })
    if (currentSeq !== fetchSeq) return
    response.value = res
    hasLoadedOnce.value = true

    if (res.enabled === false) {
      pauseAutoRefresh()
    }
  } catch (err: any) {
    if (currentSeq !== fetchSeq || isCanceledRequest(err)) return
    console.error('[OpsRealtimeRequestsCard] Failed to fetch realtime requests', err)
    errorMessage.value = err?.message || t('admin.ops.realtimeRequests.loadFailed')
    hasLoadedOnce.value = true
  } finally {
    if (currentSeq === fetchSeq) {
      loading.value = false
      if (activeController === controller) {
        activeController = null
      }
    }
  }
}

const { pause: pauseAutoRefresh, resume: resumeAutoRefresh } = useIntervalFn(
  () => {
    if (!autoRefreshEnabled.value || !isEnabled.value) return
    if (loading.value) return
    void fetchRealtimeRequests({ silent: true })
  },
  5000,
  { immediate: false }
)

function toggleAutoRefresh() {
  autoRefreshEnabled.value = !autoRefreshEnabled.value
  if (autoRefreshEnabled.value) {
    if (isEnabled.value) {
      resumeAutoRefresh()
    }
  } else {
    pauseAutoRefresh()
  }
}

function handleManualRefresh() {
  void fetchRealtimeRequests({ silent: false })
}

const debouncedApplySearch = useDebounceFn((value: string) => {
  appliedSearch.value = value
  void fetchRealtimeRequests({ silent: false })
}, 300)

watch(searchInput, (val) => {
  debouncedApplySearch(val)
})

function clearSearch() {
  searchInput.value = ''
  appliedSearch.value = ''
  void fetchRealtimeRequests({ silent: false })
}

watch([resultFilter, modelFilter, limit], () => {
  void fetchRealtimeRequests({ silent: false })
})

watch(
  () => props.refreshToken,
  (next, prev) => {
    if (next !== prev && next > 0) {
      void fetchRealtimeRequests({ silent: true })
    }
  }
)

function openDetail(item: OpsRealtimeRequestItem) {
  selectedItem.value = item
  showDetailModal.value = true
}

function formatPhaseTooltip(item: OpsRealtimeRequestItem): string {
  const { segments } = computePhaseBreakdown(item)
  const lines = segments.map(
    (seg) => `${t(seg.labelKey)}: ${formatDurationMs(seg.valueMs)} (${seg.percent.toFixed(1)}%)`
  )
  lines.push(`${t('admin.ops.realtimeRequests.phases.ttft')}: ${formatDurationMs(item.headers_end_ms)}`)
  return lines.join('\n')
}

function formatTimeTooltip(iso?: string | null): string {
  if (!iso) return ''
  return `${formatObservedDateTimeFull(iso)} (${formatRelativeTime(iso)})\n${iso}`
}

let isUnmounted = false

onMounted(async () => {
  await fetchRealtimeRequests({ silent: false })
  if (!isUnmounted && autoRefreshEnabled.value && isEnabled.value) {
    resumeAutoRefresh()
  }
})

onUnmounted(() => {
  isUnmounted = true
  pauseAutoRefresh()
  abortInFlight()
})
</script>

<template>
  <section
    id="realtime-requests"
    class="scroll-mt-20 rounded-2xl border border-ui bg-ui-surface p-4 shadow-sm dark:border-ui dark:bg-dark-900/60 md:p-5"
  >
    <!-- Header -->
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2.5">
          <h3 class="text-sm font-semibold text-ui-ink">
            {{ t('admin.ops.realtimeRequests.title') }}
          </h3>

          <!-- Buffer Status Pill -->
          <span
            v-if="response && response.enabled"
            class="inline-flex items-center gap-1.5 rounded-full bg-emerald-50 px-2.5 py-0.5 text-[11px] font-medium text-emerald-700 ring-1 ring-inset ring-emerald-600/20 dark:bg-emerald-900/25 dark:text-emerald-300 dark:ring-emerald-500/30"
          >
            <span class="h-1.5 w-1.5 rounded-full bg-emerald-500" />
            {{
              t('admin.ops.realtimeRequests.bufferStatus', {
                current: response.total_in_buffer,
                capacity: response.buffer_size,
                ttl: response.ttl_hours
              })
            }}
          </span>

          <span
            v-else-if="response && !response.enabled"
            class="inline-flex items-center gap-1.5 rounded-full bg-amber-50 px-2.5 py-0.5 text-[11px] font-medium text-amber-700 ring-1 ring-inset ring-amber-600/20 dark:bg-amber-900/25 dark:text-amber-300 dark:ring-amber-500/30"
          >
            <span class="h-1.5 w-1.5 rounded-full bg-amber-500" />
            {{ t('admin.ops.realtimeRequests.disabledBadge') }}
          </span>
        </div>
        <p class="mt-1 text-xs text-ui-muted">
          {{ t('admin.ops.realtimeRequests.description') }}
        </p>
      </div>

      <!-- Controls -->
      <div class="flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors"
          :class="
            autoRefreshEnabled && isEnabled
              ? 'border-emerald-200 bg-emerald-50 text-emerald-700 hover:bg-emerald-100 dark:border-emerald-800/60 dark:bg-emerald-900/25 dark:text-emerald-300 dark:hover:bg-emerald-900/40'
              : 'border-ui bg-ui-surface text-ui-muted hover:bg-gray-50 hover:text-ui-ink dark:hover:bg-dark-800'
          "
          :disabled="!isEnabled"
          :title="t('admin.ops.realtimeRequests.autoRefreshToggleTitle')"
          @click="toggleAutoRefresh"
        >
          <span
            class="h-2 w-2 rounded-full"
            :class="
              autoRefreshEnabled && isEnabled
                ? 'animate-pulse bg-emerald-500'
                : 'bg-gray-400 dark:bg-dark-500'
            "
          />
          <span>
            {{
              autoRefreshEnabled && isEnabled
                ? t('admin.ops.realtimeRequests.autoRefreshOn')
                : t('admin.ops.realtimeRequests.autoRefreshPaused')
            }}
          </span>
        </button>

        <button
          type="button"
          class="btn btn-secondary btn-sm inline-flex items-center gap-1.5"
          :disabled="loading"
          @click="handleManualRefresh"
        >
          <Icon name="refresh" size="xs" :class="{ 'animate-spin': loading }" />
          <span>{{ t('admin.ops.realtimeRequests.manualRefresh') }}</span>
        </button>
      </div>
    </div>

    <!-- Disabled Hint Banner -->
    <div
      v-if="response && !response.enabled"
      class="rounded-xl border border-amber-200 bg-amber-50/80 p-4 dark:border-amber-800/50 dark:bg-amber-900/20"
    >
      <div class="flex items-start gap-3">
        <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0 text-amber-600 dark:text-amber-400" />
        <div class="text-xs">
          <div class="font-semibold text-amber-900 dark:text-amber-200">
            {{ t('admin.ops.realtimeRequests.disabledTitle') }}
          </div>
          <div class="mt-1 text-amber-800 dark:text-amber-300">
            {{ t('admin.ops.realtimeRequests.disabledHint') }}
          </div>
        </div>
      </div>
    </div>

    <template v-else>
      <!-- Error Banner -->
      <div
        v-if="errorMessage"
        class="mb-4 rounded-xl bg-red-50 px-3.5 py-2.5 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400"
      >
        {{ errorMessage }}
      </div>

      <!-- 4 Summary Stat Cards -->
      <div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <div class="rounded-xl border border-ui bg-gray-50/70 px-3.5 py-2.5 dark:bg-dark-800/60">
          <div class="text-[11px] font-medium text-ui-muted">
            {{ t('admin.ops.realtimeRequests.summary.total') }}
          </div>
          <div class="mt-1 flex items-baseline justify-between gap-2">
            <span class="font-mono text-lg font-bold text-ui-ink">
              {{ summary.total.toLocaleString() }}
            </span>
            <span class="text-[11px] text-ui-muted">
              {{
                t('admin.ops.realtimeRequests.summary.totalSub', {
                  success: summary.success,
                  failure: summary.failure
                })
              }}
            </span>
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50/70 px-3.5 py-2.5 dark:bg-dark-800/60">
          <div class="text-[11px] font-medium text-ui-muted">
            {{ t('admin.ops.realtimeRequests.summary.successRate') }}
          </div>
          <div class="mt-1 font-mono text-lg font-bold" :class="successRateColorClass">
            {{ summary.total > 0 ? `${summary.success_rate.toFixed(1)}%` : '—' }}
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50/70 px-3.5 py-2.5 dark:bg-dark-800/60">
          <div class="text-[11px] font-medium text-ui-muted">
            {{ t('admin.ops.realtimeRequests.summary.avgTotalMs') }}
          </div>
          <div class="mt-1 font-mono text-lg font-bold text-ui-ink">
            {{ summary.total > 0 ? formatDurationMs(summary.avg_total_ms) : '—' }}
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50/70 px-3.5 py-2.5 dark:bg-dark-800/60">
          <div class="text-[11px] font-medium text-ui-muted">
            {{ t('admin.ops.realtimeRequests.summary.slowCount') }}
          </div>
          <div
            class="mt-1 font-mono text-lg font-bold"
            :class="
              summary.slow_count > 0
                ? 'text-amber-600 dark:text-amber-400'
                : 'text-ui-ink'
            "
          >
            {{ summary.slow_count.toLocaleString() }}
          </div>
        </div>
      </div>

      <!-- Filter Bar -->
      <div class="mb-3 flex flex-wrap items-center gap-2.5">
        <!-- Result Segmented Buttons -->
        <div
          class="inline-flex rounded-lg border border-ui bg-gray-100/80 p-0.5 dark:bg-dark-800"
          role="group"
        >
          <button
            v-for="opt in resultFilterOptions"
            :key="opt.value"
            type="button"
            class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
            :class="
              resultFilter === opt.value
                ? 'bg-ui-surface text-ui-ink shadow-sm dark:bg-dark-700'
                : 'text-ui-muted hover:text-ui-ink'
            "
            @click="resultFilter = opt.value"
          >
            {{ opt.label }}
          </button>
        </div>

        <!-- Model Selector -->
        <div class="w-44 sm:w-52">
          <Select v-model="modelFilter" :options="modelOptions" />
        </div>

        <!-- Search Input -->
        <div class="relative min-w-[200px] flex-1">
          <Icon
            name="search"
            size="sm"
            class="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-ui-muted"
          />
          <input
            v-model="searchInput"
            type="text"
            class="input w-full pl-8 pr-7 text-xs"
            :placeholder="t('admin.ops.realtimeRequests.filters.searchPlaceholder')"
          />
          <button
            v-if="searchInput"
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-ui-muted hover:text-ui-ink"
            aria-label="Clear search"
            @click="clearSearch"
          >
            <Icon name="x" size="xs" />
          </button>
        </div>

        <!-- Limit Selector -->
        <div class="w-32">
          <Select v-model="limit" :options="limitOptions" />
        </div>
      </div>

      <!-- 6-Phase Color Legend Bar -->
      <div class="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-xl border border-ui bg-gray-50/60 px-3 py-2 text-[11px] dark:bg-dark-800/50">
        <div class="flex flex-wrap items-center gap-x-3.5 gap-y-1">
          <span
            v-for="phase in REALTIME_REQUEST_PHASES"
            :key="phase.key"
            class="inline-flex items-center gap-1.5 text-ui-ink"
          >
            <span
              class="inline-block h-2.5 w-2.5 rounded-sm"
              :style="{ backgroundColor: phase.color }"
            />
            <span>{{ t(phase.labelKey) }}</span>
          </span>
        </div>
        <span class="text-ui-muted">
          {{ t('admin.ops.realtimeRequests.phases.legendNote') }}
        </span>
      </div>

      <!-- Loading (first load) -->
      <div v-if="loading && !hasLoadedOnce" class="py-12 text-center text-xs text-ui-muted">
        {{ t('common.loading') }}
      </div>

      <!-- Empty -->
      <EmptyState
        v-else-if="items.length === 0"
        :title="t('admin.ops.realtimeRequests.empty')"
        :description="t('admin.ops.realtimeRequests.emptyHint')"
      />

      <!-- Table / Mobile Cards -->
      <div v-else class="overflow-hidden rounded-xl border border-ui">
        <div class="max-h-[540px] overflow-auto">
          <!-- Mobile Card Layout (< 768px) -->
          <div v-if="!isDesktopViewport" class="divide-y divide-gray-100 dark:divide-dark-800">
            <div
              v-for="row in items"
              :key="`${row.request_id}-${row.observed_at}`"
              class="cursor-pointer space-y-2.5 p-3.5 transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/60"
              @click="openDetail(row)"
            >
              <div class="flex items-center justify-between gap-2">
                <span
                  class="font-mono text-xs font-medium text-ui-muted"
                  :title="formatTimeTooltip(row.observed_at)"
                >
                  {{ formatObservedTime(row.observed_at) }}
                </span>
                <div class="flex items-center gap-1.5">
                  <span
                    class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold"
                    :class="
                      isRequestSuccess(row)
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
                        : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
                    "
                  >
                    {{
                      isRequestSuccess(row)
                        ? t('admin.ops.realtimeRequests.status.success')
                        : getFailureReasonLabel(row.failure_reason, t)
                    }}
                  </span>
                  <span
                    v-if="row.http_status != null"
                    class="rounded px-1.5 py-0.5 font-mono text-[10px] font-semibold ring-1 ring-inset"
                    :class="getHttpStatusClass(row.http_status)"
                  >
                    {{ row.http_status }}
                  </span>
                </div>
              </div>

              <div class="text-xs">
                <div class="truncate font-medium text-ui-ink">{{ row.model || '—' }}</div>
                <div class="mt-0.5 truncate text-[11px] text-ui-muted">
                  {{ row.user_email || (row.user_id != null ? `#${row.user_id}` : '—') }}
                  · {{ row.account_name || (row.account_id != null ? `#${row.account_id}` : '—') }}
                </div>
              </div>

              <!-- Stacked 6-Phase Bar -->
              <div
                class="flex h-2 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
                :title="formatPhaseTooltip(row)"
              >
                <div
                  v-for="seg in computePhaseBreakdown(row).segments"
                  :key="seg.key"
                  v-show="seg.percent > 0"
                  class="h-full"
                  :style="{ width: `${seg.percent}%`, backgroundColor: seg.color }"
                />
              </div>

              <div class="flex items-center justify-between text-[11px] text-ui-muted">
                <span class="font-mono">
                  {{ t('admin.ops.realtimeRequests.table.totalDuration') }}:
                  <strong class="text-ui-ink">{{ formatDurationMs(row.total_ms) }}</strong>
                  · TTFT: {{ formatDurationMs(row.headers_end_ms) }}
                  · {{ formatByteSize(row.bytes_in_wire) }}
                </span>
                <button
                  type="button"
                  class="rounded-md bg-primary-50 px-2 py-0.5 text-[11px] font-semibold text-primary-700 hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-300"
                  @click.stop="openDetail(row)"
                >
                  {{ t('admin.ops.realtimeRequests.table.detail') }}
                </button>
              </div>
            </div>
          </div>

          <!-- Desktop Table (>= 768px) -->
          <table v-else class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="sticky top-0 z-10 bg-gray-50 dark:bg-dark-800/95">
              <tr>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.time') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.userKey') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.accountPlatform') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.modelEndpoint') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-right text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.requestBody') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.pipeline') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-right text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.totalDuration') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-right text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.ttft') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-left text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.status') }}
                </th>
                <th class="whitespace-nowrap px-3 py-2.5 text-right text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.table.actions') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 bg-ui-surface dark:divide-dark-700/80 dark:bg-ui-surface">
              <tr
                v-for="(row, idx) in items"
                :key="`${row.request_id}-${row.observed_at}-${idx}`"
                class="cursor-pointer transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/60"
                @click="openDetail(row)"
              >
                <!-- Time -->
                <td
                  class="whitespace-nowrap px-3 py-2.5 font-mono text-xs text-ui-muted dark:text-ui-ink"
                  :title="formatTimeTooltip(row.observed_at)"
                >
                  {{ formatObservedTime(row.observed_at) }}
                </td>

                <!-- User / API Key -->
                <td class="max-w-[190px] px-3 py-2.5 text-xs">
                  <div
                    class="truncate font-medium text-ui-ink"
                    :title="row.user_email || (row.user_id != null ? `#${row.user_id}` : '')"
                  >
                    {{ row.user_email || (row.user_id != null ? `#${row.user_id}` : '—') }}
                  </div>
                  <div
                    v-if="row.api_key_name || row.api_key_masked"
                    class="mt-0.5 truncate text-[11px] text-ui-muted"
                    :title="`${row.api_key_name || ''} ${row.api_key_masked || ''}`.trim()"
                  >
                    <span>{{ row.api_key_name || '' }}</span>
                    <span v-if="row.api_key_masked" class="ml-1 font-mono">{{ row.api_key_masked }}</span>
                  </div>
                </td>

                <!-- Account / Platform -->
                <td class="max-w-[165px] px-3 py-2.5 text-xs">
                  <div
                    class="truncate font-medium text-ui-ink"
                    :title="row.account_name || (row.account_id != null ? `#${row.account_id}` : '')"
                  >
                    {{ row.account_name || (row.account_id != null ? `#${row.account_id}` : '—') }}
                  </div>
                  <div class="mt-0.5 flex items-center gap-1.5 text-[10px]">
                    <span
                      v-if="row.platform"
                      class="rounded bg-gray-100 px-1.5 py-0.5 font-semibold uppercase text-ui-muted dark:bg-dark-700 dark:text-ui-ink"
                    >
                      {{ row.platform }}
                    </span>
                    <span v-if="row.group_name" class="truncate text-ui-muted" :title="row.group_name">
                      {{ row.group_name }}
                    </span>
                  </div>
                </td>

                <!-- Model / Endpoint -->
                <td class="max-w-[200px] px-3 py-2.5 text-xs">
                  <div class="truncate font-mono font-medium text-ui-ink" :title="row.model || ''">
                    {{ row.model || '—' }}
                  </div>
                  <div
                    v-if="row.endpoint || row.endpoint_class"
                    class="mt-0.5 truncate font-mono text-[10px] text-ui-muted"
                    :title="row.endpoint || row.endpoint_class || ''"
                  >
                    {{ row.endpoint || row.endpoint_class }}
                  </div>
                </td>

                <!-- Request Body -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right font-mono text-xs text-ui-ink">
                  {{ formatByteSize(row.bytes_in_wire) }}
                </td>

                <!-- Pipeline Stacked 6-Segment Bar -->
                <td class="whitespace-nowrap px-3 py-2.5">
                  <div class="group relative inline-block w-36 align-middle">
                    <div
                      class="flex h-2.5 w-36 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
                      :title="formatPhaseTooltip(row)"
                    >
                      <div
                        v-for="seg in computePhaseBreakdown(row).segments"
                        :key="seg.key"
                        v-show="seg.percent > 0"
                        class="h-full"
                        :style="{ width: `${seg.percent}%`, backgroundColor: seg.color }"
                      />
                    </div>
                    <!-- Rich hover tooltip -->
                    <div
                      class="pointer-events-none absolute left-1/2 z-30 hidden w-56 -translate-x-1/2 rounded-lg border border-ui bg-ui-surface p-2.5 text-[11px] shadow-lg group-hover:block dark:border-dark-600 dark:bg-dark-800"
                      :class="idx < 2 ? 'top-full mt-1.5' : 'bottom-full mb-1.5'"
                    >
                      <div class="space-y-1">
                        <div
                          v-for="seg in computePhaseBreakdown(row).segments"
                          :key="seg.key"
                          class="flex items-center justify-between gap-2"
                        >
                          <span class="flex items-center gap-1.5 text-ui-muted">
                            <span
                              class="inline-block h-2 w-2 rounded-sm"
                              :style="{ backgroundColor: seg.color }"
                            />
                            {{ t(seg.labelKey) }}
                          </span>
                          <span class="font-mono font-medium text-ui-ink">
                            {{ formatDurationMs(seg.valueMs) }}
                          </span>
                        </div>
                        <div class="mt-1 flex items-center justify-between border-t border-ui pt-1">
                          <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.phases.ttft') }}</span>
                          <span class="font-mono font-semibold text-ui-ink">
                            {{ formatDurationMs(row.headers_end_ms) }}
                          </span>
                        </div>
                      </div>
                    </div>
                  </div>
                </td>

                <!-- Total Duration -->
                <td
                  class="whitespace-nowrap px-3 py-2.5 text-right font-mono text-xs font-semibold"
                  :class="
                    (row.total_ms ?? 0) >= 3000
                      ? 'text-amber-600 dark:text-amber-400'
                      : 'text-ui-ink'
                  "
                >
                  {{ formatDurationMs(row.total_ms) }}
                </td>

                <!-- TTFT -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right font-mono text-xs text-ui-muted dark:text-ui-ink">
                  {{ formatDurationMs(row.headers_end_ms) }}
                </td>

                <!-- Status -->
                <td class="whitespace-nowrap px-3 py-2.5">
                  <div class="flex items-center gap-1.5">
                    <span
                      class="inline-flex max-w-[140px] items-center truncate rounded-full px-2 py-0.5 text-[10px] font-semibold"
                      :class="
                        isRequestSuccess(row)
                          ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
                          : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
                      "
                      :title="
                        isRequestSuccess(row)
                          ? t('admin.ops.realtimeRequests.status.success')
                          : row.failure_reason || getFailureReasonLabel(row.failure_reason, t)
                      "
                    >
                      {{
                        isRequestSuccess(row)
                          ? t('admin.ops.realtimeRequests.status.success')
                          : getFailureReasonLabel(row.failure_reason, t)
                      }}
                    </span>
                    <span
                      v-if="row.http_status != null"
                      class="font-mono text-[10px] text-ui-muted"
                    >
                      {{ row.http_status }}
                    </span>
                  </div>
                </td>

                <!-- Actions -->
                <td class="whitespace-nowrap px-3 py-2.5 text-right">
                  <button
                    type="button"
                    class="rounded-lg bg-gray-100 px-2.5 py-1 text-xs font-semibold text-ui-ink transition-colors hover:bg-primary-50 hover:text-primary-700 dark:bg-dark-700 dark:hover:bg-primary-900/30 dark:hover:text-primary-300"
                    @click.stop="openDetail(row)"
                  >
                    {{ t('admin.ops.realtimeRequests.table.detail') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- Single-Request Detail Modal -->
    <OpsRealtimeRequestDetailModal v-model:show="showDetailModal" :item="selectedItem" />
  </section>
</template>
