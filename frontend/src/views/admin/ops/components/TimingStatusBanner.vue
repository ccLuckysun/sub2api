<template>
  <div v-if="visible" class="mt-3 rounded-lg border px-3 py-2" :class="containerClass" data-testid="timing-status-banner">
    <div class="flex flex-wrap items-center gap-1.5 text-xs">
      <span class="font-medium text-ui-ink">{{ t('admin.ops.requestTiming.states.title') }}</span>
      <span
        v-for="reason in reasons"
        :key="reason"
        class="rounded px-1.5 py-0.5 text-[11px] font-semibold ring-1 ring-inset"
        :class="timingReasonBadgeClass(reason)"
        :data-testid="`timing-status-reason-${reason}`"
        :title="t(timingReasonHintKey(reason), { min: minSamples })"
      >{{ t(timingReasonLabelKey(reason)) }}</span>
      <span v-if="aggregationLabel" class="text-[11px] text-ui-muted" data-testid="timing-aggregation-status">
        {{ aggregationLabel }}
      </span>
    </div>

    <p v-if="primaryHint" class="mt-1 text-[11px] leading-relaxed text-ui-muted">
      {{ primaryHint }}
    </p>

    <dl class="mt-2 grid grid-cols-2 gap-x-3 gap-y-0.5 text-[11px] sm:grid-cols-3" data-testid="timing-status-facts">
      <div v-if="candidateText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.candidateRows') }}</dt>
        <dd class="font-mono text-ui-ink" data-testid="timing-status-candidates">{{ candidateText }}</dd>
      </div>
      <div v-if="sampleText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.actualSamples') }}</dt>
        <dd class="font-mono text-ui-ink" data-testid="timing-status-samples">{{ sampleText }}</dd>
      </div>
      <div v-if="matchedText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.matchedRows') }}</dt>
        <dd class="font-mono text-ui-ink" data-testid="timing-status-matched">{{ matchedText }}</dd>
      </div>
      <div v-if="lagText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.lag') }}</dt>
        <dd class="font-mono" :class="lagClass" data-testid="timing-status-lag">{{ lagText }}</dd>
      </div>
      <div v-if="watermarkText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.watermark') }}</dt>
        <dd class="font-mono text-ui-ink">{{ watermarkText }}</dd>
      </div>
      <div v-if="pendingText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.pendingMinutes') }}</dt>
        <dd class="font-mono text-ui-ink" data-testid="timing-status-pending">{{ pendingText }}</dd>
      </div>
      <div v-if="lastRunText" class="flex items-baseline justify-between gap-1">
        <dt class="truncate text-ui-muted">{{ t('admin.ops.requestTiming.states.lastRun') }}</dt>
        <dd class="font-mono text-ui-ink">{{ lastRunText }}</dd>
      </div>
    </dl>

    <p v-if="lastErrorText" class="mt-1 break-words text-[11px] text-rose-600 dark:text-rose-300" data-testid="timing-status-error">
      {{ t('admin.ops.requestTiming.states.lastError') }}: {{ lastErrorText }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TimingAggregation } from '@/types/requestTiming'
import {
  aggregationStatusLabelKey,
  durationParts,
  timingReasonBadgeClass,
  timingReasonHintKey,
  timingReasonLabelKey,
  type TimingReason,
} from '@/utils/timingHealth'
import { formatDateTime } from '@/views/admin/ops/utils/opsFormatters'

const fmt = (value: string | null | undefined): string => (value ? formatDateTime(value) : '')

/**
 * 并列展示「为什么这里没有数据」：五个原因可同时出现（例如聚合滞后 + 字段缺失），
 * 不再用一个空态文案覆盖全部情况。数字一律来自响应元信息，不做推断。
 */
interface Props {
  reasons: TimingReason[]
  aggregation?: TimingAggregation | null
  candidateCount?: number | null
  metricN?: number | null
  minSamples?: number
  /** 该卡自身命中的行数（慢请求/分组），与候选数区分 */
  matchedRows?: number | null
  /** always：即使没有原因也展示聚合水位（质量卡） */
  mode?: 'auto' | 'always'
}

const props = withDefaults(defineProps<Props>(), {
  aggregation: null,
  candidateCount: null,
  metricN: null,
  minSamples: 5,
  matchedRows: null,
  mode: 'auto',
})

const { t } = useI18n()

const visible = computed(
  () => props.mode === 'always' || props.reasons.length > 0 || Boolean(props.aggregation)
)

const containerClass = computed(() => {
  if (props.reasons.includes('aggregation_error')) {
    return 'border-rose-200 bg-rose-50 dark:border-rose-500/30 dark:bg-rose-500/10'
  }
  if (props.reasons.includes('unfinalized') || props.reasons.includes('fields_missing')) {
    return 'border-amber-200 bg-amber-50 dark:border-amber-500/30 dark:bg-amber-500/10'
  }
  return 'border-ui bg-ui-surface-muted dark:border-ui'
})

/** 只展示第一条原因的处置建议：多了会变成噪音，其余靠徽章标题（title）补全。 */
const primaryHint = computed(() => {
  const first = props.reasons[0]
  return first ? t(timingReasonHintKey(first), { min: props.minSamples }) : ''
})

const aggregationLabel = computed(() => {
  if (!props.aggregation) return ''
  return t(aggregationStatusLabelKey(props.aggregation.status))
})

const candidateText = computed(() =>
  props.candidateCount === null || props.candidateCount === undefined
    ? ''
    : props.candidateCount.toLocaleString()
)

const sampleText = computed(() => {
  const n = props.metricN
  if (n === null || n === undefined) return ''
  return `${n.toLocaleString()} / ${t('admin.ops.requestTiming.states.minSamples')} ${props.minSamples}`
})

const matchedText = computed(() =>
  props.matchedRows === null || props.matchedRows === undefined ? '' : props.matchedRows.toLocaleString()
)

const lagText = computed(() => {
  const parts = durationParts(props.aggregation?.lag_seconds ?? null)
  if (!parts) return ''
  return t(`admin.ops.requestTiming.states.lag${parts.unit === 'seconds' ? 'Seconds' : parts.unit === 'minutes' ? 'Minutes' : 'Hours'}`, {
    [parts.unit === 'seconds' ? 'seconds' : parts.unit === 'minutes' ? 'minutes' : 'hours']: parts.value,
  })
})

const lagClass = computed(() => {
  const seconds = props.aggregation?.lag_seconds ?? 0
  if (seconds >= 3600) return 'text-rose-600 dark:text-rose-400'
  if (seconds >= 300) return 'text-amber-600 dark:text-amber-400'
  return 'text-ui-ink'
})

const watermarkText = computed(() => {
  const value = props.aggregation?.finalized_through
  if (!value) return ''
  return fmt(value)
})

const pendingText = computed(() =>
  props.aggregation && props.aggregation.pending_minutes > 0
    ? String(props.aggregation.pending_minutes)
    : ''
)

const lastRunText = computed(() => fmt(props.aggregation?.last_run_at))
const lastErrorText = computed(() => props.aggregation?.last_error ?? '')
</script>
