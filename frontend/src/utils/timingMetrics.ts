import type { RequestTiming, RequestTimingStatus } from '@/types/requestTiming'

/**
 * 一期三段拆解的展示口径。
 *
 * 关键约定（与计划 §3.1/§6.1 一致，改动前先对照计划）：
 * - 上传耗时是差值估算（网关总耗时 − 处理耗时），文案必须带"估算"；
 * - 速率是解压后等效速率，单位 KiB/s（1024B/s），不是 kbit/s，也不是 wire bandwidth；
 * - 阈值严格大于/小于才跨档，等于阈值保持原档；
 * - NULL 一律显示 `—`，不写 0、不标红。
 */

export type TimingSeverity = 'good' | 'warn' | 'critical'

export const UPLOAD_MS_WARN = 5_000
export const UPLOAD_MS_CRITICAL = 10_000
export const UPLOAD_RATE_SLOW_KIB_S = 500
/** B/token 超过该值时提示可能含图片/工具数据（计划 §6.1）。 */
export const BYTES_PER_TOKEN_HINT = 20

export const uploadSeverity = (ms: number | null | undefined): TimingSeverity | null => {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return null
  if (ms > UPLOAD_MS_CRITICAL) return 'critical'
  if (ms > UPLOAD_MS_WARN) return 'warn'
  return 'good'
}

export const uploadRateSeverity = (kibPerSecond: number | null | undefined): TimingSeverity | null => {
  if (kibPerSecond === null || kibPerSecond === undefined || !Number.isFinite(kibPerSecond)) return null
  return kibPerSecond < UPLOAD_RATE_SLOW_KIB_S ? 'critical' : 'good'
}

export const TIMING_TEXT_CLASSES: Record<TimingSeverity, string> = {
  good: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  critical: 'text-red-600 dark:text-red-400',
}

export const TIMING_BAR_CLASSES: Record<TimingSeverity, string> = {
  good: 'bg-emerald-500',
  warn: 'bg-amber-400',
  critical: 'bg-red-500',
}

/** 三段条配色：上传段用告警色，其余两段用中性色，避免整条都被判红。 */
export const TIMING_SEGMENT_CLASSES = {
  upload: 'bg-amber-400 dark:bg-amber-500',
  uploadCritical: 'bg-red-500',
  upstreamTtft: 'bg-sky-500',
  stream: 'bg-violet-500',
} as const

export const TIMING_STATUS_META: Record<RequestTimingStatus, { labelKey: string; badgeClass: string }> = {
  complete: {
    labelKey: 'usage.timing.status.complete',
    badgeClass: 'bg-emerald-100 text-emerald-700 ring-emerald-200 dark:bg-emerald-500/20 dark:text-emerald-300 dark:ring-emerald-500/30',
  },
  partial: {
    labelKey: 'usage.timing.status.partial',
    badgeClass: 'bg-amber-100 text-amber-700 ring-amber-200 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/30',
  },
  unavailable: {
    labelKey: 'usage.timing.status.unavailable',
    badgeClass: 'bg-gray-100 text-gray-600 ring-gray-200 dark:bg-gray-500/20 dark:text-gray-300 dark:ring-gray-500/30',
  },
  invalid: {
    labelKey: 'usage.timing.status.invalid',
    badgeClass: 'bg-rose-100 text-rose-700 ring-rose-200 dark:bg-rose-500/20 dark:text-rose-300 dark:ring-rose-500/30',
  },
}

const isFiniteNumber = (value: number | null | undefined): value is number =>
  value !== null && value !== undefined && Number.isFinite(value)

/** 表格/图例用：&lt;1s 显示毫秒，其余显示秒；NULL 显示 —。 */
export const formatTimingMs = (ms: number | null | undefined): string => {
  if (!isFiniteNumber(ms)) return '—'
  if (ms < 0) return '—'
  if (ms < 1_000) return `${Math.round(ms)}ms`
  if (ms < 10_000) return `${(ms / 1_000).toFixed(2)}s`
  return `${(ms / 1_000).toFixed(1)}s`
}

/** 三段条数值用固定两位小数（与计划 §6.2 的 13.40s/14.07s/1.43s 一致）。 */
export const formatTimingSeconds = (ms: number | null | undefined): string => {
  if (!isFiniteNumber(ms) || ms < 0) return '—'
  return `${(ms / 1_000).toFixed(2)}s`
}

/** 请求体：默认 MiB（1024²），小体积保留更多有效位。 */
export const formatBodySize = (bytes: number | null | undefined): string => {
  if (!isFiniteNumber(bytes) || bytes < 0) return '—'
  const mib = bytes / (1024 * 1024)
  if (mib >= 1) return `${mib.toFixed(2)} MiB`
  const kib = bytes / 1024
  if (kib >= 1) return `${kib.toFixed(1)} KiB`
  return `${Math.round(bytes)} B`
}

export const formatBytesExact = (bytes: number | null | undefined): string => {
  if (!isFiniteNumber(bytes) || bytes < 0) return '—'
  return `${bytes.toLocaleString()} B`
}

/** 解压后等效速率（契约字段名 upload_kbps）；单位固定标注 KiB/s，避免被读成 kbit/s。 */
export const formatUploadRate = (kibPerSecond: number | null | undefined): string => {
  if (!isFiniteNumber(kibPerSecond) || kibPerSecond < 0) return '—'
  if (kibPerSecond >= 100) return `${kibPerSecond.toFixed(0)} KiB/s`
  if (kibPerSecond >= 10) return `${kibPerSecond.toFixed(1)} KiB/s`
  return `${kibPerSecond.toFixed(2)} KiB/s`
}

export const formatTokensPerSecond = (tps: number | null | undefined): string => {
  if (!isFiniteNumber(tps) || tps < 0) return '—'
  return `${tps.toFixed(1)} tok/s`
}

export const formatBytesPerToken = (value: number | null | undefined): string => {
  if (!isFiniteNumber(value) || value < 0) return '—'
  return `${value.toFixed(2)} B/token`
}

export const shouldHintLargeBytesPerToken = (value: number | null | undefined): boolean =>
  isFiniteNumber(value) && value > BYTES_PER_TOKEN_HINT

/** 慢请求 TOP 的排序键白名单（与后端保持一致）。 */
export const SLOW_TIMING_SORT_KEYS = ['upload_ms', 'upstream_ttft_ms', 'real_ttfb_ms'] as const
export type SlowTimingSortKey = (typeof SLOW_TIMING_SORT_KEYS)[number]

export const isSlowTimingSortKey = (value: unknown): value is SlowTimingSortKey =>
  typeof value === 'string' && (SLOW_TIMING_SORT_KEYS as readonly string[]).includes(value)

/** 大请求体筛选（复现 >5MiB 场景，计划 §8.1）。 */
export const LARGE_BODY_BYTES = 5 * 1024 * 1024

/** 缺失原因 → i18n key；契约只有一个 `reason` 字符串，未知值原样展示。 */
export const TIMING_REASON_LABELS: Record<string, string> = {
  clamped_total: 'usage.timing.reason.clampedTotal',
  missing_total: 'usage.timing.reason.missingTotal',
  missing_duration: 'usage.timing.reason.missingDuration',
  missing_first_token: 'usage.timing.reason.missingFirstToken',
  missing_body: 'usage.timing.reason.missingBody',
  not_http_stream: 'usage.timing.reason.notHttpStream',
  first_token_exceeds_duration: 'usage.timing.reason.firstTokenExceedsDuration',
  residual_exceeded: 'usage.timing.reason.residualExceeded',
  source_truncated: 'usage.timing.reason.sourceTruncated',
}

export const timingReasonLabelKey = (reason: string): string | null =>
  TIMING_REASON_LABELS[reason] ?? null

/** 供详情抽屉判断是否值得显示"不完整"提示。 */
export const isTimingDegraded = (timing: RequestTiming | null): boolean =>
  !!timing && timing.timing_status !== 'complete'
