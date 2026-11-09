/**
 * 请求耗时趋势的桶选择（计划 §6.3 / 契约）。
 *
 * 契约只接受 60|300|900 秒；UI 先按范围挑最小可用桶，保证点数 ≤ 720，
 * 避免把 720 个点以上的序列渲染进图表（也避免触发服务端点上限）。
 */

export type TimingBucketSeconds = 60 | 300 | 900

export const TIMING_MAX_POINTS = 720

const RANGE_MINUTES: Record<string, number> = {
  '5m': 5,
  '30m': 30,
  '1h': 60,
  '6h': 360,
  '24h': 1440,
  '7d': 10080,
}

/**
 * 自定义时间范围的分钟跨度；无效/缺失返回 null。
 *
 * 对齐服务端桶边界：从 floor(start) 到 ceil(end) 覆盖，避免不足一分钟的
 * 端点把跨度算小一档（进而选出偏细的桶、多出第 721 个点）。
 */
export const customRangeMinutes = (start?: string | null, end?: string | null): number | null => {
  if (!start || !end) return null
  const s = new Date(start).getTime()
  const e = new Date(end).getTime()
  if (!Number.isFinite(s) || !Number.isFinite(e) || e <= s) return null
  const floorStart = Math.floor(s / 60000) * 60000
  const ceilEnd = Math.ceil(e / 60000) * 60000
  return Math.max(1, Math.round((ceilEnd - floorStart) / 60000))
}

export const rangeMinutes = (
  timeRange: string,
  customStart?: string | null,
  customEnd?: string | null
): number => {
  const custom = customRangeMinutes(customStart, customEnd)
  if (custom !== null) return custom
  return RANGE_MINUTES[timeRange] ?? 60
}

/**
 * 选桶（与后端一致）：60s ≤12h、300s ≤24h、900s 更长（7d → 672 点）。
 *
 * 24h 以上必须降到 900s：后端对 >24h 的 300s 请求会拒绝，不能用「点数还够」
 * 作为选 300s 的理由。
 */
export const pickTimingBucketSeconds = (
  timeRange: string,
  customStart?: string | null,
  customEnd?: string | null
): TimingBucketSeconds => {
  const minutes = rangeMinutes(timeRange, customStart, customEnd)
  if (minutes <= 12 * 60) return 60
  if (minutes <= 24 * 60) return 300
  return 900
}

/**
 * 组装时间窗口参数：**只有自定义范围**才携带绝对时间。
 *
 * 预设范围必须忽略残留的 customStart/customEnd —— 从自定义 7 天切回「近 1 小时」时，
 * 若继续把这些时间当成查询窗口，后端仍按 7 天取数，「缩短范围」永远无法脱离预算错误。
 */
export const timingWindowParams = (
  timeRange: string,
  customStart?: string | null,
  customEnd?: string | null
): { bucket_seconds: TimingBucketSeconds; start_time?: string; end_time?: string } => {
  const isCustom = timeRange === 'custom'
  const start = isCustom ? customStart : null
  const end = isCustom ? customEnd : null
  const window: { bucket_seconds: TimingBucketSeconds; start_time?: string; end_time?: string } = {
    bucket_seconds: pickTimingBucketSeconds(timeRange, start, end),
  }
  if (start && end) {
    window.start_time = start
    window.end_time = end
  }
  return window
}

/** 图表纵轴单位换算：body_bytes → MiB（1024²）。 */
export const bytesToMiB = (bytes: number | null | undefined): number | null =>
  bytes === null || bytes === undefined || !Number.isFinite(bytes) ? null : bytes / (1024 * 1024)

/**
 * 每桶最小样本：图表默认 5（与告警 min_samples=30 是两个不同口径，
 * 计划 §6.3 明确分开，不要复用告警默认值）。
 */
export const TIMING_CHART_MIN_SAMPLES = 5

/**
 * 分位序列（trend）的样本数藏在各分位对象里，分组序列（groups）在点顶层；
 * 用可选 `samples` 访问器统一，避免调用方各自复制样本过滤逻辑。
 */
export const toChartSeries = <T extends { bucket_start: string }>(
  points: T[],
  pick: (point: T, index: number) => number | null | undefined,
  samples: (point: T) => number | null | undefined = (point) => (point as { n?: number | null }).n ?? null,
  minSamples = TIMING_CHART_MIN_SAMPLES
): (number | null)[] =>
  points.map((point, index) => {
    const n = samples(point)
    if (n === null || n === undefined || !Number.isFinite(n) || n < minSamples) return null
    const value = pick(point, index)
    return value === null || value === undefined || !Number.isFinite(value) ? null : value
  })

/**
 * hist 模式的百分位可能无值但有 bounds；图表用中点，tooltip 用区间。
 *
 * 上界缺失代表落在末尾 overflow 桶（真实值可能远大于 lo），把 lo 当点会画成
 * 正常值，因此返回 null 让曲线断开。
 */
export const boundsMidpoint = (bounds: { lo: number; hi: number | null } | null | undefined): number | null => {
  if (!bounds) return null
  if (bounds.hi === null || !Number.isFinite(bounds.hi)) return null
  return (bounds.lo + bounds.hi) / 2
}
