/**
 * 请求耗时面板的「为什么没有图/没有值」分类。
 *
 * 旧实现只有一个空态文案（「当前范围暂无有效样本」），把五种互不相同的情况混成一句，
 * 运维无法判断是该补数据、该等待聚合、还是该修采集。这里把原因拆成可并列的枚举：
 *
 * - no_requests          该范围内根本没有候选请求
 * - fields_missing       有请求但缺字段（旧行没有 gateway_latency_ms / body_bytes 等）
 * - insufficient_samples 有值但每桶样本不足（图表最小 5）
 * - unfinalized          聚合水位没追上（桶还没闭合）
 * - aggregation_error    聚合失败/滞后/schema 不符
 *
 * 一份响应可能同时命中多条（例如「有请求 + 水位落后 + 字段缺失」），因此对外一律是数组，
 * 由 `orderTimingReasons` 按「先归因、后等待」的固定顺序排序，保证四张卡呈现一致。
 */
export const TIMING_REASONS = [
  'aggregation_error',
  'unfinalized',
  'fields_missing',
  'no_requests',
  'insufficient_samples',
] as const

export type TimingReason = (typeof TIMING_REASONS)[number]

/** 展示顺序：错误优先（需要人介入），其次等待聚合，再次数据形态问题，最后样本量。 */
const REASON_ORDER: Record<TimingReason, number> = {
  aggregation_error: 0,
  unfinalized: 1,
  fields_missing: 2,
  no_requests: 3,
  insufficient_samples: 4,
}

/** 每条原因的语义级别，决定徽章配色：error 需要排查，warn 需要等待/改善，info 只是说明。 */
export const TIMING_REASON_TONE: Record<TimingReason, 'error' | 'warn' | 'info'> = {
  aggregation_error: 'error',
  unfinalized: 'warn',
  fields_missing: 'warn',
  no_requests: 'info',
  insufficient_samples: 'info',
}

export const isTimingReason = (value: unknown): value is TimingReason =>
  typeof value === 'string' && (TIMING_REASONS as readonly string[]).includes(value)

/**
 * 去重 + 固定顺序。未知 code 直接丢弃（后端新增原因时宁可少显示，也不要显示原始英文 code）。
 */
export const orderTimingReasons = (codes: readonly (string | null | undefined)[]): TimingReason[] => {
  const seen = new Set<TimingReason>()
  for (const code of codes) {
    if (isTimingReason(code)) seen.add(code)
  }
  return [...seen].sort((a, b) => REASON_ORDER[a] - REASON_ORDER[b])
}

/** i18n key：`admin.ops.requestTiming.states.<reason>`（中英同步维护）。 */
export const timingReasonLabelKey = (reason: TimingReason): string =>
  `admin.ops.requestTiming.states.${reason}`

/** i18n key：每段原因补充的「该做什么」提示。 */
export const timingReasonHintKey = (reason: TimingReason): string =>
  `admin.ops.requestTiming.states.hints.${reason}`

export const timingReasonBadgeClass = (reason: TimingReason): string => {
  switch (TIMING_REASON_TONE[reason]) {
    case 'error':
      return 'bg-rose-100 text-rose-700 ring-rose-200 dark:bg-rose-500/20 dark:text-rose-300 dark:ring-rose-500/30'
    case 'warn':
      return 'bg-amber-100 text-amber-700 ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30'
    default:
      return 'bg-ui-surface-muted text-ui-muted ring-ui dark:ring-ui'
  }
}

/** 样本量不足的通用判据（图表断线阈值），与 `TIMING_CHART_MIN_SAMPLES` 保持一致。 */
export const TIMING_MIN_CHART_SAMPLES = 5

export interface TimingSampleHint {
  /** 该图在该范围里实际参与绘制的最大桶样本数 */
  actualMaxSamples: number
  /** 有值的桶数 */
  bucketsWithValue: number
  /** 总桶数 */
  bucketCount: number
  minSamples: number
}

/**
 * 单图样本说明文案，例如「实际最大 n=3，最小 5（12/24 桶有值）」。
 * 组件只负责取值，措辞集中在 i18n，避免四张卡各写一套。
 */
export const sampleHintParams = (hint: TimingSampleHint) => ({
  actual: hint.actualMaxSamples,
  min: hint.minSamples,
  withValue: hint.bucketsWithValue,
  total: hint.bucketCount,
})

import type { TimingAggregation, TimingQuality } from '@/types/requestTiming'

export interface TimingWindowEvidence {
  /** 同过滤候选行数（quality.candidate_count）；未知传 null */
  candidateCount: number | null
  /** 该指标在本窗口内有值的样本数（例如三条曲线各自的 Σn） */
  metricN: number | null
  /** 图表/质量门槛，默认 5 */
  minSamples?: number
  /** 窗口内是否存在待补写（dirty）桶 */
  hasPendingBuckets?: boolean
  /**
   * true=已确认整窗定稿；false=明确未定稿（水位没覆盖）；null/undefined=未知。
   *
   * 未知不能被当成「未定稿」也不能被当成「已定稿」：前者会误报等待，后者会误报 no_requests。
   */
  windowFinalized?: boolean | null
  /** 每桶 n>=minSamples 的桶数（不是全窗 Σn：60 个桶各 1 条也不能画线） */
  drawableBuckets?: number | null
  /** 本窗口总桶数 */
  totalBuckets?: number | null
  /** 单桶最大 n（用于「每桶样本不足」的具体数字） */
  maxBucketN?: number | null
}

export interface TimingStatus {
  reasons: TimingReason[]
  lagSeconds: number | null
  pendingMinutes: number
  candidateCount: number | null
  metricN: number | null
  minSamples: number
  drawableBuckets: number | null
  totalBuckets: number | null
  maxBucketN: number | null
}

const aggregationReasons = (aggregation: TimingAggregation | null | undefined): TimingReason[] => {
  if (!aggregation) return []
  switch (aggregation.status) {
    // 聚合失败 / 水位严重落后 / 从未运行：都是需要人介入的「聚合异常」，
    // 具体差多少由 lag_seconds 与 pending_minutes 表达，不在这里臆造。
    case 'error':
    case 'stale':
    case 'not_started':
      return ['aggregation_error']
    case 'catching_up':
      return ['unfinalized']
    default:
      return []
  }
}

/**
 * 由聚合健康 + 窗口内候选/样本事实推导可并列的原因。
 *
 * 关键约束（repair 契约）：
 * - 未定稿/待补写的窗口**永远不能**判成 `no_requests`；
 * - `candidate>0` 且指标 n 少于候选 → `fields_missing`（附实际 n/candidate），
 *   与样本量无关：n=3、candidate=12 同时命中 fields_missing 与 insufficient_samples；
 * - 样本量按**每桶**判：全窗 Σn>=5 但每个桶只有 1 条时仍然是不能连线，
 *   所以除 Σn<min 外，`drawableBuckets===0` 也算 `insufficient_samples`；
 * - 只有「已确认定稿且候选为 0」才是 `no_requests`（`windowFinalized===null` 不可断言）。
 */
export const deriveTimingStatus = (
  aggregation: TimingAggregation | null | undefined,
  evidence: TimingWindowEvidence
): TimingStatus => {
  const minSamples = evidence.minSamples ?? TIMING_MIN_CHART_SAMPLES
  const candidate = evidence.candidateCount
  const metricN = evidence.metricN
  const pending = evidence.hasPendingBuckets === true
  const finalized = evidence.windowFinalized ?? null
  const drawable = evidence.drawableBuckets ?? null
  const totalBuckets = evidence.totalBuckets ?? null
  const maxBucketN = evidence.maxBucketN ?? null

  const reasons: TimingReason[] = [...aggregationReasons(aggregation)]

  // 水位没覆盖整窗（finalized=false）与存在待补写桶，都是「还没定稿」。
  if ((pending || finalized === false) && !reasons.includes('unfinalized')) {
    reasons.push('unfinalized')
  }

  if (candidate !== null && candidate > 0 && (metricN === null || metricN < candidate)) {
    reasons.push('fields_missing')
  }

  const thinTotal = metricN !== null && metricN > 0 && metricN < minSamples
  const thinBuckets = drawable !== null && drawable === 0 && metricN !== null && metricN > 0
  if (thinTotal || thinBuckets) {
    reasons.push('insufficient_samples')
  }

  if (candidate === 0 && finalized === true && !reasons.includes('unfinalized')) {
    reasons.push('no_requests')
  }

  const ordered = orderTimingReasons(reasons)
  // 未定稿时不允许只留 no_requests（防御：正常路径不会同时出现）
  const safe = ordered.includes('unfinalized') || ordered.includes('aggregation_error')
    ? ordered.filter((reason) => reason !== 'no_requests')
    : ordered

  return {
    reasons: safe,
    lagSeconds: aggregation?.lag_seconds ?? null,
    pendingMinutes: aggregation?.pending_minutes ?? 0,
    candidateCount: candidate,
    metricN,
    minSamples,
    drawableBuckets: drawable,
    totalBuckets,
    maxBucketN,
  }
}

/** 聚合健康的展示文案键（与五个原因分开：status 有 not_started/healthy 这类额外态）。 */
export const aggregationStatusLabelKey = (status: TimingAggregation['status']): string =>
  `admin.ops.requestTiming.aggregation.${status}`

/** 把秒数拆成 i18n 单位（<90s 秒、<90m 分钟、否则小时，保留 1 位小数）。 */
export const durationParts = (seconds: number | null | undefined) => {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return null
  if (seconds < 90) return { unit: 'seconds' as const, value: Math.round(seconds) }
  if (seconds < 90 * 60) return { unit: 'minutes' as const, value: Math.round(seconds / 60) }
  return { unit: 'hours' as const, value: Math.round((seconds / 3600) * 10) / 10 }
}

/** 从 quality 取候选与指标样本数（指标样本数由调用方按曲线决定）。 */
export const candidateCountOf = (quality: TimingQuality | null | undefined): number | null =>
  quality && Number.isFinite(quality.candidate_count) ? quality.candidate_count : null
