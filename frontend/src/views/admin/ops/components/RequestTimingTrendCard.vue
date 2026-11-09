<template>
  <div class="card p-4" data-testid="request-timing-trend-card">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div>
        <div class="flex items-center gap-1.5">
          <h3 class="text-sm font-semibold text-ui-ink">{{ t('admin.ops.requestTiming.trend.title') }}</h3>
          <HelpTooltip :text="t('admin.ops.requestTiming.trend.hint')" />
        </div>
        <p class="mt-0.5 text-xs text-ui-muted">{{ t('admin.ops.requestTiming.trend.subtitle') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-1.5 text-[11px]">
        <span v-if="trend" class="rounded bg-ui-surface-muted px-1.5 py-0.5 font-mono text-ui-muted">
          {{ trend.mode }} · {{ trend.bucket_seconds }}s
        </span>
        <span
          v-if="trend?.provisional"
          class="rounded bg-amber-100 px-1.5 py-0.5 font-semibold text-amber-700 ring-1 ring-inset ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30"
          :title="t('admin.ops.requestTiming.trend.provisionalHint')"
        >{{ t('admin.ops.requestTiming.trend.provisional') }}</span>
        <span v-if="trend?.degradation_reason" class="text-amber-600 dark:text-amber-400">
          {{ trend.degradation_reason }}
        </span>
      </div>
    </div>

    <div v-if="error" class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
      {{ error }}
    </div>

    <TimingStatusBanner
      v-else
      :reasons="cardStatus.reasons"
      :aggregation="trend?.aggregation ?? null"
      :candidate-count="candidateCount"
      :metric-n="null"
      :min-samples="TIMING_CHART_MIN_SAMPLES"
    />

    <div class="mt-3 grid gap-4 lg:grid-cols-3">
      <div v-for="chart in charts" :key="chart.key" class="min-h-[200px]">
        <div class="mb-1 flex items-center justify-between text-xs">
          <div class="flex items-center gap-1 font-medium text-ui-ink">
            <span>{{ chart.title }}</span>
            <HelpTooltip :text="chart.hint" />
          </div>
          <div class="flex items-center gap-2">
            <span class="font-mono text-[11px] text-ui-muted" :data-testid="`timing-trend-samples-${chart.key}`">{{ chart.samplesText }}</span>
            <span class="text-ui-muted">{{ chart.unit }}</span>
          </div>
        </div>
        <div v-if="loading && !trend" class="h-[180px] animate-pulse rounded-lg bg-ui-surface-muted" />
        <div
          v-else-if="!chart.hasData"
          class="flex h-[180px] flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed border-ui px-3 text-center text-xs text-ui-muted dark:border-ui"
          :data-testid="`timing-trend-empty-${chart.key}`"
        >
          <div class="flex flex-wrap items-center justify-center gap-1">
            <span
              v-for="reason in chart.status.reasons"
              :key="reason"
              class="rounded px-1.5 py-0.5 text-[11px] font-semibold ring-1 ring-inset"
              :class="timingReasonBadgeClass(reason)"
              :data-testid="`timing-trend-reason-${chart.key}-${reason}`"
            >{{ t(timingReasonLabelKey(reason)) }}</span>
          </div>
          <p class="max-w-[300px] leading-relaxed" :data-testid="`timing-trend-empty-hint-${chart.key}`">{{ chart.emptyHint }}</p>
        </div>
        <div v-else class="h-[180px]" :data-testid="`timing-trend-chart-${chart.key}`">
          <Line :ref="chart.setRef" :data="chart.data" :options="chart.options" />
        </div>
      </div>
    </div>

    <div class="mt-2 space-y-0.5 text-[11px] text-ui-muted">
      <p>{{ t('admin.ops.requestTiming.trend.gapHint', { min: TIMING_CHART_MIN_SAMPLES }) }}</p>
      <p v-if="watermarkLine" class="font-mono" data-testid="timing-trend-watermark">{{ watermarkLine }}</p>
      <p v-if="pendingLine" class="text-amber-600 dark:text-amber-400" data-testid="timing-trend-pending">{{ pendingLine }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LineElement,
  LinearScale,
  PointElement,
  Title,
  Tooltip,
  type ChartData,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import { useChartTheme } from '@/composables/useChartTheme'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { getRequestTimingTrend, type OpsTimingQueryParams } from '@/api/admin/ops'
import type { TimingAggregation, TimingQuantiles, RequestTimingTrendResponse } from '@/types/requestTiming'
import { formatHistoryLabel } from '../utils/opsFormatters'
import {
  TIMING_CHART_MIN_SAMPLES,
  boundsMidpoint,
  timingWindowParams,
  toChartSeries,
} from '@/utils/timingBucket'
import { TIMING_POINT_HOVER_RADIUS, timingPointRadius } from './timingChartPoints'
import TimingStatusBanner from './TimingStatusBanner.vue'
import {
  deriveTimingStatus,
  durationParts,
  timingReasonBadgeClass,
  timingReasonHintKey,
  timingReasonLabelKey,
  type TimingStatus,
} from '@/utils/timingHealth'
import { formatDateTime } from '../utils/opsFormatters'

ChartJS.register(Title, Tooltip, Legend, LineElement, LinearScale, PointElement, CategoryScale, Filler)

interface Props {
  timeRange: string
  platform?: string
  groupId?: number | null
  customStartTime?: string | null
  customEndTime?: string | null
  queryMode?: 'auto' | 'raw' | 'preagg'
  /** 与 dashboard 共享刷新节拍 */
  refreshToken?: number
}

const props = withDefaults(defineProps<Props>(), {
  platform: '',
  groupId: null,
  customStartTime: null,
  customEndTime: null,
  queryMode: 'auto',
  refreshToken: 0,
})

const { t } = useI18n()
const chartTheme = useChartTheme()

/** 供 OpsDashboard 把同一份聚合健康下发给分组/慢请求/质量卡，避免四张卡各自猜。 */
const emit = defineEmits<{ (e: 'aggregation', value: TimingAggregation | null): void }>()

const trend = ref<RequestTimingTrendResponse | null>(null)
const loading = ref(false)
const error = ref('')

let seq = 0
let controller: AbortController | null = null

const colors = computed(() => ({
  blue: '#3b82f6',
  amber: '#f59e0b',
  red: '#ef4444',
  grid: chartTheme.colors.value.grid,
  text: chartTheme.colors.value.text,
}))

const buildParams = (): OpsTimingQueryParams => ({
  time_range: props.timeRange as OpsTimingQueryParams['time_range'],
  // 自定义范围才带绝对时间：预设范围残留的旧自定义时间会把窗口拉回去。
  ...timingWindowParams(props.timeRange, props.customStartTime, props.customEndTime),
  platform: props.platform || undefined,
  group_id: props.groupId ?? undefined,
  query_mode: props.queryMode,
})

const load = async () => {
  controller?.abort()
  const c = new AbortController()
  controller = c
  const current = ++seq
  loading.value = true
  error.value = ''
  try {
    const result = await getRequestTimingTrend(buildParams(), { signal: c.signal })
    if (current !== seq) return
    trend.value = result
    emit('aggregation', result.aggregation ?? null)
  } catch (err) {
    if (current !== seq) return
    const e = err as { name?: string; code?: string; message?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    trend.value = null
    emit('aggregation', null)
    error.value = e?.message ?? t('admin.ops.requestTiming.loadFailed')
  } finally {
    if (current === seq) loading.value = false
  }
}

/** 分位优先取点值，hist 无值则退回 bounds 中点（tooltip 展示区间）。 */
const quantileValue = (q: TimingQuantiles | undefined, key: 'p10' | 'p50' | 'p90' | 'p95'): number | null => {
  if (!q) return null
  const direct = q[key]
  if (direct !== null && direct !== undefined) return direct
  return boundsMidpoint(q[`${key}_bounds`])
}

interface ChartSpec {
  key: string
  title: string
  hint: string
  unit: string
  hasData: boolean
  /** 该图自身的样本事实与原因（与卡片级 banner 分开：每张图的 n 不同） */
  status: TimingStatus
  samplesText: string
  emptyHint: string
  data: ChartData<'line', (number | null)[], string>
  options: Record<string, unknown>
  setRef: (el: unknown) => void
}

const baseOptions = (unit: string) => {
  const c = colors.value
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: chartTheme.animation.value,
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: {
        position: 'top' as const,
        align: 'end' as const,
        labels: { color: c.text, usePointStyle: true, boxWidth: 6, font: { size: 10 } },
      },
      tooltip: {
        ...chartTheme.tooltip.value,
        callbacks: {
          label: (ctx: { dataset: { label?: string }; parsed: { y: number | null } }) => {
            const value = ctx.parsed.y
            if (value === null) return `${ctx.dataset.label ?? ''}: —`
            return `${ctx.dataset.label ?? ''}: ${value.toLocaleString(undefined, { maximumFractionDigits: 2 })} ${unit}`
          },
        },
      },
    },
    scales: {
      x: {
        grid: { display: false },
        ticks: { color: c.text, font: { size: 10 }, maxRotation: 0, autoSkip: true, maxTicksLimit: 8 },
      },
      y: {
        grid: { color: c.grid },
        ticks: { color: c.text, font: { size: 10 } },
        beginAtZero: true,
      },
    },
  }
}

const labels = computed(() =>
  (trend.value?.points ?? []).map((p) => formatHistoryLabel(p.bucket_start, props.timeRange))
)

const series = (
  label: string,
  color: string,
  values: (number | null)[],
  fill = false
) => ({
  label,
  data: values,
  borderColor: color,
  backgroundColor: `${color}20`,
  fill,
  tension: 0.3,
  spanGaps: false,
  // 有效点给最小半径：孤立桶（前后都断线）也要看得见；null 点不可见，断线语义不变。
  pointRadius: timingPointRadius,
  pointHoverRadius: TIMING_POINT_HOVER_RADIUS,
  pointHitRadius: 8,
})

type QuantileKey = 'p10' | 'p50' | 'p90' | 'p95'

const points = computed(() => trend.value?.points ?? [])
const candidateCount = computed(() => trend.value?.quality?.candidate_count ?? null)

/** 与脏分钟相交的桶：即使全局水位健康，这些桶也不算定稿。 */
const hasPendingBuckets = computed(() => points.value.some((point) => point.pending === true))

/** 旧服务不发送 finalized 字段：未知时不推断「整窗已定稿」，避免错误宣称 no_requests。 */
const finalizedKnown = computed(
  () => points.value.length > 0 && points.value.every((point) => typeof point.finalized === 'boolean')
)
/**
 * null = 旧服务没给 finalized 字段，无法断言；
 * false = 有 pending 桶或明确有未定稿桶；true = 每个桶都 finalized 且无 pending。
 */
const windowFinalized = computed<boolean | null>(() => {
  if (hasPendingBuckets.value) return false
  if (!finalizedKnown.value) return null
  return points.value.every((point) => point.finalized === true)
})

const cardStatus = computed<TimingStatus>(() =>
  deriveTimingStatus(trend.value?.aggregation ?? null, {
    candidateCount: candidateCount.value,
    metricN: null,
    hasPendingBuckets: hasPendingBuckets.value,
    windowFinalized: windowFinalized.value,
  })
)

/**
 * 不能只看全窗 Σn 判断「能不能画」：60 个桶各 1 条时 Σn=60，但每桶都低于最小 5，
 * 图上依旧是空白。因此同时统计可绘制桶数（每桶 n>=5）与单桶最大值。
 */
const bucketStats = (field: 'upload_ms' | 'upload_kbps' | 'body_bytes') => {
  const list = points.value
  const sum = list.reduce((total, point) => total + (point[field]?.n ?? 0), 0)
  let drawable = 0
  let maxBucketN = 0
  for (const point of list) {
    const n = point[field]?.n ?? 0
    if (n > maxBucketN) maxBucketN = n
    if (n >= TIMING_CHART_MIN_SAMPLES) drawable += 1
  }
  return { sum, drawable, maxBucketN, total: list.length }
}

const chartStatus = (field: 'upload_ms' | 'upload_kbps' | 'body_bytes'): TimingStatus => {
  const stats = bucketStats(field)
  return deriveTimingStatus(trend.value?.aggregation ?? null, {
    candidateCount: candidateCount.value,
    metricN: stats.sum,
    hasPendingBuckets: hasPendingBuckets.value,
    windowFinalized: windowFinalized.value,
    drawableBuckets: stats.drawable,
    totalBuckets: stats.total,
    maxBucketN: stats.maxBucketN,
  })
}

/** 头部样本说明：总量 + 真正能连线的桶数（用户要求的「每图实际 n」）。 */
const samplesText = (field: 'upload_ms' | 'upload_kbps' | 'body_bytes'): string => {
  const stats = bucketStats(field)
  return t('admin.ops.requestTiming.states.samplesBucketInline', {
    n: stats.sum.toLocaleString(),
    drawable: stats.drawable,
    total: stats.total,
    min: TIMING_CHART_MIN_SAMPLES,
  })
}

/** 空图时唯一一句解释：优先第一条原因的处置建议，元信息缺失则明确说「无法判断」。 */
const emptyHint = (status: TimingStatus, field: 'upload_ms' | 'upload_kbps' | 'body_bytes'): string => {
  // raw 模式下的「无观测」是原始查询的事实，不能写成后端判定「没有请求」。
  if (status.reasons.includes('no_requests') && trend.value?.mode === 'raw_exact') {
    return t('admin.ops.requestTiming.trend.emptyRaw', {
      lag: lagText.value || '—',
    })
  }
  const first = status.reasons[0]
  if (!first) return t('admin.ops.requestTiming.trend.emptyUnknown')
  const stats = bucketStats(field)
  // 每桶样本不足与总量不足要分开说：前者是断线的主要来源。
  if (first === 'insufficient_samples' && stats.drawable === 0 && stats.sum > 0) {
    return t('admin.ops.requestTiming.states.hints.insufficient_samples_buckets', {
      max: stats.maxBucketN.toLocaleString(),
      min: status.minSamples,
      total: stats.total,
      n: stats.sum.toLocaleString(),
    })
  }
  if ((first === 'fields_missing' || status.reasons.includes('fields_missing')) && status.candidateCount !== null) {
    return t('admin.ops.requestTiming.states.hints.fields_missing_with_counts', {
      n: (status.metricN ?? 0).toLocaleString(),
      candidate: status.candidateCount.toLocaleString(),
    })
  }
  return t(timingReasonHintKey(first), { min: status.minSamples })
}

const lagText = computed(() => {
  const aggregation = trend.value?.aggregation
  if (!aggregation) return ''
  const parts = durationParts(aggregation.lag_seconds)
  if (!parts) return '—'
  return t(`admin.ops.requestTiming.states.lag${parts.unit === 'seconds' ? 'Seconds' : parts.unit === 'minutes' ? 'Minutes' : 'Hours'}`, {
    [parts.unit]: parts.value,
  })
})

const watermarkLine = computed(() => {
  const aggregation = trend.value?.aggregation
  if (!aggregation) return ''
  const lag = lagText.value || '—'
  const watermark = aggregation.finalized_through ? formatDateTime(aggregation.finalized_through) : '—'
  return t('admin.ops.requestTiming.states.watermarkChain', { watermark, lag })
})

const pendingLine = computed(() => {
  const count = points.value.filter((point) => point.pending === true).length
  if (!count) return ''
  return t('admin.ops.requestTiming.states.pendingBuckets', { count })
})

const samplesOf = (point: RequestTimingTrendResponse['points'][number], field: 'upload_ms' | 'upload_kbps' | 'body_bytes' | 'real_ttfb_ms'): number | null =>
  point[field]?.n ?? null

const uploadSeries = (key: QuantileKey) =>
  toChartSeries(
    trend.value?.points ?? [],
    (point) => quantileValue(point.upload_ms, key),
    (point) => samplesOf(point, 'upload_ms')
  )

const rateSeries = (key: QuantileKey) =>
  toChartSeries(
    trend.value?.points ?? [],
    (point) => quantileValue(point.upload_kbps, key),
    (point) => samplesOf(point, 'upload_kbps')
  )

const bodySeries = (key: QuantileKey) =>
  toChartSeries(
    trend.value?.points ?? [],
    (point) => {
      const bytes = quantileValue(point.body_bytes, key)
      return bytes === null ? null : bytes / (1024 * 1024)
    },
    (point) => samplesOf(point, 'body_bytes')
  )

const charts = computed<ChartSpec[]>(() => {
  const c = colors.value
  const uploadChart = {
    labels: labels.value,
    datasets: [
      series('p95', c.red, uploadSeries('p95')),
      series('p90', c.amber, uploadSeries('p90')),
      series('p50', c.blue, uploadSeries('p50'), true),
    ],
  }
  const rateChart = {
    labels: labels.value,
    datasets: [
      series('p50', c.blue, rateSeries('p50'), true),
      series('p10', c.amber, rateSeries('p10')),
    ],
  }
  const bodyChart = {
    labels: labels.value,
    datasets: [
      series('p95', c.red, bodySeries('p95')),
      series('p50', c.blue, bodySeries('p50'), true),
    ],
  }

  const hasAny = (data: { datasets: { data: (number | null)[] }[] }) =>
    data.datasets.some((d) => d.data.some((v) => v !== null))

  return [
    {
      key: 'upload',
      title: t('admin.ops.requestTiming.trend.upload'),
      hint: t('admin.ops.requestTiming.trend.uploadHint'),
      unit: 'ms',
      hasData: hasAny(uploadChart),
      status: chartStatus('upload_ms'),
      samplesText: samplesText('upload_ms'),
      emptyHint: emptyHint(chartStatus('upload_ms'), 'upload_ms'),
      data: uploadChart,
      options: baseOptions('ms'),
      setRef: () => {},
    },
    {
      key: 'rate',
      title: t('admin.ops.requestTiming.trend.rate'),
      hint: t('admin.ops.requestTiming.trend.rateHint'),
      unit: 'KiB/s',
      hasData: hasAny(rateChart),
      status: chartStatus('upload_kbps'),
      samplesText: samplesText('upload_kbps'),
      emptyHint: emptyHint(chartStatus('upload_kbps'), 'upload_kbps'),
      data: rateChart,
      options: baseOptions('KiB/s'),
      setRef: () => {},
    },
    {
      key: 'body',
      title: t('admin.ops.requestTiming.trend.body'),
      hint: t('admin.ops.requestTiming.trend.bodyHint'),
      unit: 'MiB',
      hasData: hasAny(bodyChart),
      status: chartStatus('body_bytes'),
      samplesText: samplesText('body_bytes'),
      emptyHint: emptyHint(chartStatus('body_bytes'), 'body_bytes'),
      data: bodyChart,
      options: baseOptions('MiB'),
      setRef: () => {},
    },
  ]
})

watch(
  () => [props.timeRange, props.platform, props.groupId, props.customStartTime, props.customEndTime, props.queryMode, props.refreshToken],
  () => { void load() },
  { immediate: true }
)
</script>
