<template>
  <div class="card p-4" data-testid="slow-request-top-card">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div>
        <div class="flex items-center gap-1.5">
          <h3 class="text-sm font-semibold text-ui-ink">{{ t('admin.ops.requestTiming.slowTop.title') }}</h3>
          <HelpTooltip :text="t('admin.ops.requestTiming.slowTop.hint')" />
        </div>
        <p class="mt-0.5 text-xs text-ui-muted">
          {{ t('admin.ops.requestTiming.slowTop.subtitle', { count: items.length }) }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <Select v-model="sort" :options="sortOptions" size="sm" data-testid="slow-timing-sort" />
        <label class="flex items-center gap-1 text-xs text-ui-muted">
          <input v-model="largeBodyOnly" type="checkbox" class="rounded border-ui" data-testid="slow-timing-large-body" />
          {{ t('admin.ops.requestTiming.slowTop.largeBodyOnly') }}
        </label>
      </div>
    </div>

    <div
      v-if="budgetExceeded"
      class="mt-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      data-testid="slow-timing-budget"
    >
      {{ t('admin.ops.requestTiming.slowTop.budgetBody') }}
    </div>

    <div v-else-if="error" class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
      {{ error }}
    </div>

    <TimingStatusBanner
      v-else
      :reasons="status.reasons"
      :aggregation="aggregation"
      :candidate-count="candidateCount"
      :min-samples="5"
    />

    <div class="mt-3 overflow-x-auto">
      <table class="w-full min-w-[720px] text-xs">
        <thead class="text-left text-ui-muted">
          <tr class="border-b border-ui dark:border-ui">
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colTime') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colModel') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colUpload') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colRate') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colBody') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colUpstreamTtft') }}</th>
            <th class="py-1.5 pr-2 font-medium">{{ t('admin.ops.requestTiming.slowTop.colRealTtfb') }}</th>
            <th class="py-1.5 font-medium">{{ t('admin.ops.requestTiming.slowTop.colStatus') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading && !items.length">
            <td colspan="8" class="py-6 text-center text-ui-muted">{{ t('common.loading') }}</td>
          </tr>
          <tr v-else-if="!items.length && !budgetExceeded">
            <td colspan="8" class="py-6 text-center" data-testid="slow-timing-empty">
              <div class="flex flex-wrap items-center justify-center gap-1">
                <span
                  v-for="reason in status.reasons"
                  :key="reason"
                  class="rounded px-1.5 py-0.5 text-[11px] font-semibold ring-1 ring-inset"
                  :class="timingReasonBadgeClass(reason)"
                  :data-testid="`slow-timing-reason-${reason}`"
                >{{ t(timingReasonLabelKey(reason)) }}</span>
              </div>
              <p class="mt-1 text-ui-muted">{{ emptyHint }}</p>
            </td>
          </tr>
          <tr
            v-for="item in items"
            :key="item.id"
            class="cursor-pointer border-b border-ui/60 transition-colors hover:bg-ui-surface-muted dark:border-ui/60"
            :data-testid="`slow-timing-row-${item.id}`"
            tabindex="0"
            role="button"
            @click="emit('openDetail', item)"
            @keydown.enter="emit('openDetail', item)"
            @keydown.space.prevent="emit('openDetail', item)"
          >
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono text-[11px] text-ui-muted">
              {{ formatTimestamp(item.created_at) }}
            </td>
            <td class="max-w-[160px] truncate py-1.5 pr-2 text-ui-ink" :title="item.model ?? ''">
              {{ item.model ?? '—' }}
            </td>
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono" :class="uploadSeverityClass(item.timing)" :data-testid="`slow-upload-${item.id}`">
              {{ formatTimingSeconds(item.timing?.upload_ms) }}
            </td>
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono" :class="rateSeverityClass(item.timing)" :data-testid="`slow-rate-${item.id}`">
              {{ formatUploadRate(item.timing?.upload_kbps) }}
            </td>
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono" :data-testid="`slow-body-${item.id}`">
              <span :class="item.timing?.body_bytes && item.timing.body_bytes >= LARGE_BODY_BYTES ? 'text-amber-600 dark:text-amber-400' : ''">
                {{ formatBodySize(item.timing?.body_bytes) }}
              </span>
            </td>
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono" :data-testid="`slow-upstream-${item.id}`">{{ formatTimingSeconds(item.timing?.upstream_ttft_ms) }}</td>
            <td class="whitespace-nowrap py-1.5 pr-2 font-mono" :data-testid="`slow-real-${item.id}`">{{ formatTimingSeconds(item.timing?.real_ttfb_ms) }}</td>
            <td class="whitespace-nowrap py-1.5">
              <span
                class="rounded px-1.5 py-0.5 text-[10px] font-semibold"
                :class="statusClass(item.timing)"
              >{{ item.timing?.timing_status ?? 'unavailable' }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Select from '@/components/common/Select.vue'
import { isRequestTimingBudgetExceeded, listSlowTimingRequests } from '@/api/admin/ops'
import type {
  RequestTimingDetail,
  RequestTimingSlowTopResponse,
  SlowTimingSortKey,
  TimingAggregation,
  TimingQuality,
} from '@/types/requestTiming'
import { timingWindowParams } from '@/utils/timingBucket'
import TimingStatusBanner from './TimingStatusBanner.vue'
import {
  deriveTimingStatus,
  timingReasonBadgeClass,
  timingReasonHintKey,
  timingReasonLabelKey,
  type TimingStatus,
} from '@/utils/timingHealth'
import {
  LARGE_BODY_BYTES,
  TIMING_STATUS_META,
  TIMING_TEXT_CLASSES,
  formatBodySize,
  formatTimingSeconds,
  formatUploadRate,
  uploadRateSeverity,
  uploadSeverity,
} from '@/utils/timingMetrics'

interface Props {
  timeRange: string
  platform?: string
  groupId?: number | null
  customStartTime?: string | null
  customEndTime?: string | null
  refreshToken?: number
  /** 由趋势卡下发的聚合健康（与趋势卡同 scope）；缺失时用响应自带值 */
  aggregation?: TimingAggregation | null
}

const props = withDefaults(defineProps<Props>(), {
  platform: '',
  groupId: null,
  customStartTime: null,
  customEndTime: null,
  refreshToken: 0,
  aggregation: null,
})

const emit = defineEmits<{
  (e: 'openDetail', item: RequestTimingDetail): void
}>()

const { t } = useI18n()

const items = ref<RequestTimingDetail[]>([])
const response = ref<RequestTimingSlowTopResponse | null>(null)
const loading = ref(false)
const error = ref('')
const budgetExceeded = ref(false)
const sort = ref<SlowTimingSortKey>('upload_ms')
const largeBodyOnly = ref(false)

let seq = 0
let controller: AbortController | null = null

/** 响应自带 quality/aggregation（root 新增，可缺）；父级下发的聚合健康优先。 */
const quality = computed<TimingQuality | null>(() => response.value?.quality ?? null)
const aggregation = computed<TimingAggregation | null>(
  () => props.aggregation ?? response.value?.aggregation ?? null
)
const candidateCount = computed(() => quality.value?.candidate_count ?? null)

/**
 * 慢请求卡只说明「当前筛选下没有匹配行」或聚合状态，绝不宣称业务没有请求；
 * windowFinalized: null 保证不会产出 no_requests（本卡没有逐桶 finalized 元信息）。
 */
const status = computed<TimingStatus>(() =>
  deriveTimingStatus(aggregation.value, {
    candidateCount: candidateCount.value,
    metricN: null,
    hasPendingBuckets: false,
    // null = 本卡没有逐桶 finalized 元信息：既不断言定稿（不产出 no_requests），
    // 也不谎报未定稿（避免空表上出现「等待聚合」的假原因）。
    windowFinalized: null,
  })
)

const emptyHint = computed(() => {
  const first = status.value.reasons[0]
  if (first) return t(timingReasonHintKey(first), { min: 5 })
  if (candidateCount.value === 0) return t('admin.ops.requestTiming.states.noBusinessTrafficClaim')
  return t('admin.ops.requestTiming.states.matchedNone')
})

const sortOptions = computed(() => [
  { value: 'upload_ms', label: t('admin.ops.requestTiming.slowTop.sortUpload') },
  { value: 'upstream_ttft_ms', label: t('admin.ops.requestTiming.slowTop.sortUpstreamTtft') },
  { value: 'real_ttfb_ms', label: t('admin.ops.requestTiming.slowTop.sortRealTtfb') },
])

const formatTimestamp = (value: string | undefined): string => {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

const uploadSeverityClass = (timing: RequestTimingDetail['timing']): string =>
  timing ? TIMING_TEXT_CLASSES[uploadSeverity(timing.upload_ms) ?? 'good'] : ''

const rateSeverityClass = (timing: RequestTimingDetail['timing']): string =>
  timing ? TIMING_TEXT_CLASSES[uploadRateSeverity(timing.upload_kbps) ?? 'good'] : ''

const statusClass = (timing: RequestTimingDetail['timing']): string => {
  const status = timing?.timing_status
  if (!status) return 'bg-ui-surface-muted text-ui-muted'
  return TIMING_STATUS_META[status]?.badgeClass ?? 'bg-ui-surface-muted text-ui-muted'
}

const load = async () => {
  controller?.abort()
  const c = new AbortController()
  controller = c
  const current = ++seq
  loading.value = true
  error.value = ''
  budgetExceeded.value = false
  try {
    const result = await listSlowTimingRequests(
      {
        time_range: props.timeRange as never,
        ...timingWindowParams(props.timeRange, props.customStartTime, props.customEndTime),
        platform: props.platform || undefined,
        group_id: props.groupId ?? undefined,
        sort: sort.value,
        body_bytes_min: largeBodyOnly.value ? LARGE_BODY_BYTES + 1 : undefined,
      },
      { signal: c.signal }
    )
    if (current !== seq) return
    response.value = result
    items.value = result.items ?? []
  } catch (err) {
    if (current !== seq) return
    const e = err as { name?: string; code?: string; message?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    items.value = []
    response.value = null
    // 422 预算保护在 7d 等长窗口必然出现；用本地化提示替代后端英文原文，不伪装成空数据。
    if (isRequestTimingBudgetExceeded(err)) {
      budgetExceeded.value = true
      return
    }
    error.value = e?.message ?? t('admin.ops.requestTiming.loadFailed')
  } finally {
    if (current === seq) loading.value = false
  }
}

// sort / filter 改变后立即重查（不等待 dashboard 节拍）
watch([sort, largeBodyOnly], () => { void load() })

watch(
  () => [props.timeRange, props.platform, props.groupId, props.customStartTime, props.customEndTime, props.refreshToken],
  () => { void load() },
  { immediate: true }
)

defineExpose({ reload: load })
</script>
