<template>
  <div class="card p-4" data-testid="request-timing-quality-card">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div>
        <div class="flex items-center gap-1.5">
          <h3 class="text-sm font-semibold text-ui-ink">{{ t('admin.ops.requestTiming.quality.title') }}</h3>
          <HelpTooltip :text="t('admin.ops.requestTiming.quality.hint')" />
        </div>
        <p class="mt-0.5 text-xs text-ui-muted">{{ t('admin.ops.requestTiming.quality.subtitle') }}</p>
      </div>
      <span v-if="trend" class="rounded bg-ui-surface-muted px-1.5 py-0.5 text-[11px] font-mono text-ui-muted">
        {{ trend.mode }}
      </span>
    </div>

    <div v-if="error" class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
      {{ error }}
    </div>

    <TimingStatusBanner
      v-else
      mode="always"
      :reasons="status.reasons"
      :aggregation="aggregation"
      :candidate-count="candidateCount"
      :min-samples="5"
    />

    <div v-if="loading && !quality" class="mt-3 h-[120px] animate-pulse rounded-lg bg-ui-surface-muted" />

    <div
      v-else-if="!quality"
      class="mt-3 flex h-[120px] items-center justify-center rounded-lg border border-dashed border-ui px-3 text-center text-xs text-ui-muted dark:border-ui"
      data-testid="timing-quality-empty"
    >
      {{ t('admin.ops.requestTiming.quality.emptyUnknown') }}
    </div>

    <template v-else>
      <div class="mt-3">
        <div class="flex items-baseline justify-between text-xs">
          <span class="text-ui-muted">{{ t('admin.ops.requestTiming.quality.coverage') }}</span>
          <span class="font-mono text-sm" :class="coverageClass">{{ coverageText }}</span>
        </div>
        <div class="mt-1 h-2 overflow-hidden rounded-full bg-ui-surface-muted">
          <div
            class="h-full rounded-full transition-all"
            :class="coverageBarClass"
            :style="{ width: `${coveragePercent}%` }"
          />
        </div>
        <p v-if="quality.candidate_count === 0" class="mt-1 text-[11px] text-ui-muted" data-testid="timing-quality-no-candidates">
          {{ t('admin.ops.requestTiming.quality.noCandidates') }}
        </p>
      </div>

      <dl class="mt-3 grid grid-cols-2 gap-x-3 gap-y-1 text-[11px] sm:grid-cols-3">
        <div v-for="row in rows" :key="row.key" class="flex items-baseline justify-between gap-1">
          <dt class="truncate text-ui-muted" :title="row.label">{{ row.label }}</dt>
          <dd class="font-mono text-ui-ink">{{ row.value }}</dd>
        </div>
      </dl>

      <div v-if="collectorRows.length" class="mt-3 border-t border-ui pt-2 dark:border-ui" data-testid="timing-collector">
        <div class="flex items-baseline justify-between text-[11px] text-ui-muted">
          <span>{{ t('admin.ops.requestTiming.quality.collectorTitle') }}</span>
          <span class="text-[10px]">{{ t('admin.ops.requestTiming.quality.collectorScope') }}</span>
        </div>
        <dl class="mt-1 grid grid-cols-2 gap-x-3 gap-y-0.5 text-[11px] sm:grid-cols-3">
          <div v-for="row in collectorRows" :key="row.key" class="flex items-baseline justify-between gap-1">
            <dt class="truncate text-ui-muted" :title="row.label">{{ row.label }}</dt>
            <dd class="font-mono" :class="row.value ? 'text-ui-ink' : 'text-ui-muted'">{{ row.value }}</dd>
          </div>
        </dl>
      </div>

      <div class="mt-3 space-y-0.5 border-t border-ui pt-2 text-[11px] text-ui-muted dark:border-ui">
        <div v-if="finalizedText" class="flex justify-between gap-2">
          <span>{{ t('admin.ops.requestTiming.quality.finalizedThrough') }}</span>
          <span class="font-mono">{{ finalizedText }}</span>
        </div>
        <div v-if="computedText" class="flex justify-between gap-2">
          <span>{{ t('admin.ops.requestTiming.quality.computedAt') }}</span>
          <span class="font-mono">{{ computedText }}</span>
        </div>
        <div v-if="trend?.provisional && !aggregation" class="text-amber-600 dark:text-amber-400">
          {{ t('admin.ops.requestTiming.quality.provisional') }}
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { getRequestTimingTrend, type OpsTimingQueryParams } from '@/api/admin/ops'
import type { TimingAggregation, RequestTimingTrendResponse } from '@/types/requestTiming'
import { timingWindowParams } from '@/utils/timingBucket'
import TimingStatusBanner from './TimingStatusBanner.vue'
import { deriveTimingStatus, type TimingStatus } from '@/utils/timingHealth'

interface Props {
  timeRange: string
  platform?: string
  groupId?: number | null
  customStartTime?: string | null
  customEndTime?: string | null
  queryMode?: 'auto' | 'raw' | 'preagg'
  refreshToken?: number
  /** 由趋势卡下发的聚合健康（同一份数据，避免各卡各自推断） */
  aggregation?: TimingAggregation | null
}

const props = withDefaults(defineProps<Props>(), {
  platform: '',
  groupId: null,
  customStartTime: null,
  customEndTime: null,
  queryMode: 'auto',
  refreshToken: 0,
  aggregation: null,
})

const { t } = useI18n()

const trend = ref<RequestTimingTrendResponse | null>(null)
const loading = ref(false)
const error = ref('')

let seq = 0
let controller: AbortController | null = null

const quality = computed(() => trend.value?.quality ?? null)

/** 优先用父级下发的聚合健康：它来自按桶取数的趋势卡，口径唯一。 */
const aggregation = computed<TimingAggregation | null>(() => props.aggregation ?? trend.value?.aggregation ?? null)

const candidateCount = computed(() => quality.value?.candidate_count ?? null)

const status = computed<TimingStatus>(() =>
  deriveTimingStatus(aggregation.value, {
    candidateCount: candidateCount.value,
    metricN: null,
    hasPendingBuckets: trend.value?.provisional === true,
    // 没有逐桶 finalized 字段时保守视为「未确认定稿」，不允许据此宣称没有请求
    // 没有趋势响应就不做定稿断言（null），否则会把「未知」显示成「等待聚合」
    windowFinalized: trend.value === null ? null : trend.value.provisional === false,
  })
)

// 采集器计数只在响应里带 collector 时展示（旧服务没有该字段），且明确标注是「当前节点」。
const COLLECTOR_FIELDS = [
  'queue_depth',
  'enqueued',
  'applied',
  'dropped_queue_full',
  'dropped_stopped',
  'patch_misses',
  'failed_batches',
  'exhausted_batches',
  'latency_overflow',
] as const

const collectorRows = computed(() => {
  const collector = trend.value?.collector
  if (!collector) return []
  return COLLECTOR_FIELDS.filter((key) => collector[key] !== undefined && collector[key] !== null).map((key) => ({
    key,
    label: t(`admin.ops.requestTiming.quality.collector.${key}`),
    value: String(collector[key]),
  }))
})

const load = async () => {
  controller?.abort()
  const c = new AbortController()
  controller = c
  const current = ++seq
  loading.value = true
  error.value = ''
  try {
    const result = await getRequestTimingTrend(
      {
        time_range: props.timeRange as OpsTimingQueryParams['time_range'],
        ...timingWindowParams(props.timeRange, props.customStartTime, props.customEndTime),
        platform: props.platform || undefined,
        group_id: props.groupId ?? undefined,
        query_mode: props.queryMode,
      },
      { signal: c.signal }
    )
    if (current !== seq) return
    trend.value = result
  } catch (err) {
    if (current !== seq) return
    const e = err as { name?: string; code?: string; message?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    trend.value = null
    error.value = e?.message ?? t('admin.ops.requestTiming.loadFailed')
  } finally {
    if (current === seq) loading.value = false
  }
}

watch(
  () => [props.timeRange, props.platform, props.groupId, props.customStartTime, props.customEndTime, props.queryMode, props.refreshToken],
  () => { void load() },
  { immediate: true }
)

const coveragePercent = computed(() => {
  const ratio = quality.value?.coverage_ratio
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return 0
  return Math.max(0, Math.min(100, Math.round(ratio * 100)))
})

const coverageText = computed(() => {
  if (quality.value?.coverage_ratio === null || quality.value?.coverage_ratio === undefined) return '—'
  return `${coveragePercent.value}%`
})

// 与告警的 min_coverage=0.95 对齐，仅作视觉提示；不在此处判定告警
const coverageClass = computed(() => {
  const ratio = quality.value?.coverage_ratio
  if (ratio === null || ratio === undefined) return 'text-ui-muted'
  if (ratio >= 0.95) return 'text-emerald-600 dark:text-emerald-400'
  if (ratio >= 0.8) return 'text-amber-600 dark:text-amber-400'
  return 'text-rose-600 dark:text-rose-400'
})

const coverageBarClass = computed(() => {
  const ratio = quality.value?.coverage_ratio
  if (ratio === null || ratio === undefined) return 'bg-ui-muted'
  if (ratio >= 0.95) return 'bg-emerald-500'
  if (ratio >= 0.8) return 'bg-amber-500'
  return 'bg-rose-500'
})

const missingCount = computed(() => {
  const q = quality.value
  if (!q || q.candidate_count === null) return null
  return Math.max(0, q.candidate_count - q.complete_count)
})

const num = (value: number | null | undefined): string =>
  value === null || value === undefined ? '—' : value.toLocaleString()

const rows = computed(() => {
  const q = quality.value
  if (!q) return []
  return [
    { key: 'candidate', label: t('admin.ops.requestTiming.quality.candidate'), value: num(q.candidate_count) },
    { key: 'complete', label: t('admin.ops.requestTiming.quality.complete'), value: num(q.complete_count) },
    { key: 'missing', label: t('admin.ops.requestTiming.quality.missing'), value: num(missingCount.value) },
    { key: 'invalid', label: t('admin.ops.requestTiming.quality.invalid'), value: num(q.invalid_count) },
    { key: 'upload', label: t('admin.ops.requestTiming.quality.upload'), value: num(q.upload_count) },
    { key: 'ttft', label: t('admin.ops.requestTiming.quality.ttft'), value: num(q.ttft_count) },
    { key: 'real', label: t('admin.ops.requestTiming.quality.real'), value: num(q.real_count) },
    { key: 'rate', label: t('admin.ops.requestTiming.quality.rate'), value: num(q.rate_count) },
    { key: 'body', label: t('admin.ops.requestTiming.quality.body'), value: num(q.known_body_count) },
    { key: 'large', label: t('admin.ops.requestTiming.quality.largeBody'), value: num(q.large_body_count) },
  ]
})

const formatDateTime = (value: string | null | undefined): string => {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const finalizedText = computed(() => formatDateTime(trend.value?.finalized_through))
const computedText = computed(() => formatDateTime(trend.value?.computed_at))
</script>
