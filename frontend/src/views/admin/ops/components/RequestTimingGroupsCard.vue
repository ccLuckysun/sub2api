<template>
  <div class="card p-4" data-testid="request-timing-groups-card">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div>
        <div class="flex items-center gap-1.5">
          <h3 class="text-sm font-semibold text-ui-ink">{{ t('admin.ops.requestTiming.groups.title') }}</h3>
          <HelpTooltip :text="t('admin.ops.requestTiming.groups.hint')" />
        </div>
        <p class="mt-0.5 text-xs text-ui-muted">{{ t('admin.ops.requestTiming.groups.subtitle') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Select v-model="dimension" :options="dimensionOptions" size="sm" data-testid="timing-groups-dimension" />
        <input
          v-model="entities"
          type="text"
          class="w-40 rounded-lg border border-ui bg-ui-surface px-2 py-1 text-xs text-ui-ink placeholder:text-ui-muted dark:border-ui"
          :placeholder="t('admin.ops.requestTiming.groups.entitiesPlaceholder')"
          :title="t('admin.ops.requestTiming.groups.entitiesHint')"
          @keyup.enter="reload"
        />
      </div>
    </div>

    <div
      v-if="budgetExceeded"
      class="mt-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      data-testid="timing-groups-budget"
    >
      <div class="font-medium">{{ t('admin.ops.requestTiming.groups.budgetTitle') }}</div>
      <div class="mt-1">{{ t('admin.ops.requestTiming.groups.budgetBody') }}</div>
      <button type="button" class="btn btn-ghost btn-xs mt-2" @click="$emit('shortenRange')">
        {{ t('admin.ops.requestTiming.groups.budgetAction') }}
      </button>
    </div>

    <div v-else-if="error" class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200">
      {{ error }}
    </div>

    <TimingStatusBanner
      v-else
      :reasons="status.reasons"
      :aggregation="aggregation"
      :candidate-count="candidateCount"
      :matched-rows="matchedRows"
      :min-samples="5"
    />

    <div class="mt-3 h-[240px]">
      <div v-if="loading && !groups" class="h-full animate-pulse rounded-lg bg-ui-surface-muted" />
      <div
        v-else-if="!hasData"
        class="flex h-full flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed border-ui px-3 text-center text-xs text-ui-muted dark:border-ui"
        data-testid="timing-groups-empty"
      >
        <div class="flex flex-wrap items-center justify-center gap-1">
          <span
            v-for="reason in status.reasons"
            :key="reason"
            class="rounded px-1.5 py-0.5 text-[11px] font-semibold ring-1 ring-inset"
            :class="timingReasonBadgeClass(reason)"
            :data-testid="`timing-groups-reason-${reason}`"
          >{{ t(timingReasonLabelKey(reason)) }}</span>
        </div>
        <p class="max-w-[320px] leading-relaxed">{{ emptyHint }}</p>
      </div>
      <Line v-else :data="chartData" :options="chartOptions" />
    </div>

    <p class="mt-2 text-[11px] text-ui-muted">{{ t('admin.ops.requestTiming.groups.footnote') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  CategoryScale,
  Chart as ChartJS,
  Legend,
  LineElement,
  LinearScale,
  PointElement,
  Title,
  Tooltip,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import { useChartTheme } from '@/composables/useChartTheme'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Select from '@/components/common/Select.vue'
import {
  getRequestTimingGroups,
  isRequestTimingBudgetExceeded,
  type OpsTimingQueryParams,
} from '@/api/admin/ops'
import type { RequestTimingGroupsResponse, TimingAggregation, TimingQuality } from '@/types/requestTiming'
import { formatHistoryLabel } from '../utils/opsFormatters'
import { timingWindowParams, toChartSeries } from '@/utils/timingBucket'
import TimingStatusBanner from './TimingStatusBanner.vue'
import {
  deriveTimingStatus,
  timingReasonBadgeClass,
  timingReasonHintKey,
  timingReasonLabelKey,
  type TimingStatus,
} from '@/utils/timingHealth'
import { TIMING_POINT_HOVER_RADIUS, timingPointRadius } from './timingChartPoints'

ChartJS.register(Title, Tooltip, Legend, LineElement, LinearScale, PointElement, CategoryScale)

interface Props {
  timeRange: string
  platform?: string
  groupId?: number | null
  customStartTime?: string | null
  customEndTime?: string | null
  refreshToken?: number
  /** 由趋势卡下发的聚合健康（同一份数据；缺失时用响应自带值） */
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

const emit = defineEmits<{ (e: 'shortenRange'): void }>()
const { t } = useI18n()
const chartTheme = useChartTheme()

const dimension = ref<'account' | 'model'>('account')
const entities = ref('')
const groups = ref<RequestTimingGroupsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const budgetExceeded = ref(false)

let seq = 0
let controller: AbortController | null = null

const dimensionOptions = computed(() => [
  { value: 'account', label: t('admin.ops.requestTiming.groups.dimensionAccount') },
  { value: 'model', label: t('admin.ops.requestTiming.groups.dimensionModel') },
])

const PALETTE = ['#3b82f6', '#f59e0b', '#10b981', '#ef4444', '#8b5cf6', '#06b6d4', '#ec4899', '#84cc16', '#f97316', '#64748b']

const load = async () => {
  controller?.abort()
  const c = new AbortController()
  controller = c
  const current = ++seq
  loading.value = true
  error.value = ''
  budgetExceeded.value = false
  try {
    const params: Parameters<typeof getRequestTimingGroups>[0] = {
      time_range: props.timeRange as OpsTimingQueryParams['time_range'],
      ...timingWindowParams(props.timeRange, props.customStartTime, props.customEndTime),
      platform: props.platform || undefined,
      group_id: props.groupId ?? undefined,
      dimension: dimension.value,
      top: 5,
      entities: entities.value.trim() || undefined,
    }
    const result = await getRequestTimingGroups(params, { signal: c.signal })
    if (current !== seq) return
    groups.value = result
  } catch (err) {
    if (current !== seq) return
    const e = err as { name?: string; code?: string; status?: number; message?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    groups.value = null
    // 只有预算超限（明确 code 的 422）才显示预算卡；其它 422 是普通错误，
    // 否则会把参数/权限问题误标成"请缩短时间范围"。
    if (isRequestTimingBudgetExceeded(e)) {
      budgetExceeded.value = true
      error.value = ''
      return
    }
    error.value = e?.message ?? t('admin.ops.requestTiming.loadFailed')
  } finally {
    if (current === seq) loading.value = false
  }
}

const reload = () => { void load() }

watch(dimension, () => { void load() })

watch(
  () => [props.timeRange, props.platform, props.groupId, props.customStartTime, props.customEndTime, props.refreshToken],
  () => { void load() },
  { immediate: true }
)

const chartData = computed(() => {
  const seriesList = groups.value?.series ?? []
  const labels = labelsFrom(seriesList)
  return {
    labels,
    datasets: seriesList.map((s, index) => ({
      label: `${s.label} (n=${s.sample_count})`,
      // 每桶 n>=5 才画（与趋势卡同一守卫）；不能用 series.sample_count 这类整条序列的样本数代替。
      data: toChartSeries(s.points, (p) => p.p95, (p) => p.n),
      borderColor: PALETTE[index % PALETTE.length],
      backgroundColor: `${PALETTE[index % PALETTE.length]}20`,
      tension: 0.3,
      spanGaps: false,
      // 与趋势卡一致：孤立有效桶必须可见（pointRadius 0 会把有样本画成空图）。
      pointRadius: timingPointRadius,
      pointHoverRadius: TIMING_POINT_HOVER_RADIUS,
      pointHitRadius: 8,
    })),
  }
})

function labelsFrom(seriesList: RequestTimingGroupsResponse['series']): string[] {
  const all = new Set<string>()
  for (const s of seriesList) for (const p of s.points) all.add(p.bucket_start)
  return [...all].sort().map((b) => formatHistoryLabel(b, props.timeRange))
}

const hasData = computed(() => (groups.value?.series ?? []).some((s) => s.points.some((p) => p.p95 !== null)))

/** 响应自带 quality/aggregation（root 新增，可缺）；父级下发的聚合健康优先。 */
const quality = computed<TimingQuality | null>(() => groups.value?.quality ?? null)
const aggregation = computed<TimingAggregation | null>(() => props.aggregation ?? groups.value?.aggregation ?? null)
const candidateCount = computed(() => quality.value?.candidate_count ?? null)
const matchedRows = computed(() => quality.value?.candidate_count ?? null)

/**
 * 分组卡不猜「有没有业务请求」：它只能说明当前筛选与聚合状态。
 * 因此这里传 windowFinalized: null —— 永远不产出 no_requests，也不谎报未定稿。
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

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  animation: chartTheme.animation.value,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: {
    legend: {
      position: 'top' as const,
      align: 'end' as const,
      labels: { color: chartTheme.colors.value.text, usePointStyle: true, boxWidth: 6, font: { size: 10 } },
    },
    tooltip: {
      ...chartTheme.tooltip.value,
      callbacks: {
        label: (ctx: { dataset: { label?: string }; parsed: { y: number | null } }) =>
          ctx.parsed.y === null
            ? `${ctx.dataset.label ?? ''}: —`
            : `${ctx.dataset.label ?? ''}: ${ctx.parsed.y.toLocaleString(undefined, { maximumFractionDigits: 0 })} ms`,
      },
    },
  },
  scales: {
    x: {
      grid: { display: false },
      ticks: { color: chartTheme.colors.value.text, font: { size: 10 }, maxRotation: 0, autoSkip: true, maxTicksLimit: 8 },
    },
    y: {
      grid: { color: chartTheme.colors.value.grid },
      ticks: { color: chartTheme.colors.value.text, font: { size: 10 } },
      beginAtZero: true,
      title: { display: true, text: 'p95 (ms)', color: chartTheme.colors.value.text, font: { size: 10 } },
    },
  },
}))

defineExpose({ reload, emit })
</script>
