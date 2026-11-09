/**
 * 请求耗时三段拆解（一期 legacy_difference_v1）前端契约。
 *
 * 唯一 shape 来源：/tmp/sub2api-request-timing-api.md（冻结契约）。
 * 字段全部 snake_case；可空数值显式 null；UI 允许旧服务缺字段，
 * 但缺失一律按"不可用"处理，绝不补 0 或伪造派生值。
 */

export type RequestTimingStatus = 'complete' | 'partial' | 'unavailable' | 'invalid'

export interface RequestTiming {
  gateway_latency_ms: number | null
  body_bytes: number | null
  duration_ms: number | null
  first_token_ms: number | null
  /** 差值估算 = GREATEST(gateway_latency_ms - duration_ms, 0) */
  upload_ms: number | null
  upstream_ttft_ms: number | null
  stream_ms: number | null
  /** 真实首字估算 = upload_ms + first_token_ms */
  real_ttfb_ms: number | null
  /** 解压后等效速率 KiB/s（1024B/s），字段名沿用契约 upload_kbps */
  upload_kbps: number | null
  bytes_per_token: number | null
  tps_total: number | null
  tps_stream: number | null
  raw_upload_ms: number | null
  timing_residual_ms: number | null
  ctx_tokens: number | null
  timing_status: RequestTimingStatus
  timing_method: 'legacy_difference_v1' | string | null
  reason: string | null
  is_http_stream: boolean
  is_complete_timing: boolean
  is_valid_upload: boolean
  is_valid_ttft: boolean
  is_valid_real: boolean
  is_valid_rate: boolean
  is_valid_body: boolean
}

/** 单桶分位；hist 模式值可能为 null（桶未闭合/无界），此时用 bounds 判包含区间。 */
export interface TimingBounds {
  lo: number
  hi: number | null
  overflow: boolean
}

export interface TimingQuantiles {
  n: number
  p10: number | null
  p50: number | null
  p90: number | null
  p95: number | null
  p10_bounds: TimingBounds | null
  p50_bounds: TimingBounds | null
  p90_bounds: TimingBounds | null
  p95_bounds: TimingBounds | null
}

export interface TimingQuality {
  candidate_count: number
  complete_count: number
  invalid_count: number
  upload_count: number
  ttft_count: number
  real_count: number
  rate_count: number
  known_body_count: number
  large_body_count: number
  /** 0..1；无候选时为 null（不是 0） */
  coverage_ratio: number | null
}

/**
 * 聚合健康（repair 契约）：解释「有请求但没图」。
 * `lag_seconds` = max(target_through - finalized_through, 0)，与 computed_at 无关。
 */
export type TimingAggregationStatus = 'healthy' | 'catching_up' | 'stale' | 'error' | 'not_started'

export interface TimingAggregation {
  status: TimingAggregationStatus
  server_time: string
  /** floor(now - 2m)，排他闭合水位 */
  target_through: string
  finalized_through: string | null
  lag_seconds: number | null
  last_run_at: string | null
  last_success_at: string | null
  last_error_at: string | null
  last_error: string | null
  pending_minutes: number
  oldest_pending_at: string | null
}

export interface RequestTimingTrendPoint {
  bucket_start: string
  quality: TimingQuality
  upload_ms: TimingQuantiles
  upload_kbps: TimingQuantiles
  body_bytes: TimingQuantiles
  real_ttfb_ms: TimingQuantiles
  /** 桶区间结束 <= 水位且不与脏分钟相交才为 true */
  finalized?: boolean
  /** 该桶与待补写（dirty）分钟相交 */
  pending?: boolean
}

/**
 * Aggregator counters reported by the node that answered the request. Optional: older
 * servers do not send it, and it is per-process (not a fleet-wide total).
 */
export interface RequestTimingCollector {
  queue_depth?: number | null
  enqueued?: number | null
  applied?: number | null
  dropped_queue_full?: number | null
  dropped_stopped?: number | null
  patch_misses?: number | null
  failed_batches?: number | null
  exhausted_batches?: number | null
  latency_overflow?: number | null
}

export interface RequestTimingTrendResponse {
  mode: 'raw_exact' | 'hist_approx'
  hist_version: number
  bucket_seconds: number
  start_time: string
  end_time: string
  finalized_through: string | null
  computed_at: string | null
  provisional: boolean
  degradation_reason: string | null
  quality: TimingQuality
  collector?: RequestTimingCollector | null
  /** 旧服务不发送；缺失时前端不得推断聚合健康 */
  aggregation?: TimingAggregation | null
  points: RequestTimingTrendPoint[]
}

export interface RequestTimingGroupPoint {
  bucket_start: string
  n: number
  p95: number | null
}

export interface RequestTimingGroupSeries {
  key: string
  label: string
  sample_count: number
  points: RequestTimingGroupPoint[]
}

export interface RequestTimingGroupsResponse {
  mode: 'raw_exact'
  dimension: 'account' | 'model'
  bucket_seconds: number
  start_time: string
  end_time: string
  /** 同过滤 raw 候选的质量元信息（root 新增，可缺） */
  quality?: TimingQuality | null
  /** 与趋势卡共享的聚合健康（root 新增，可缺） */
  aggregation?: TimingAggregation | null
  series: RequestTimingGroupSeries[]
}

/** 慢请求 TOP 行：与 usage 详情同构的 timing，抽屉无需再请求 /usage/:id/timing。 */
export interface RequestTimingDetail {
  id: number
  created_at: string
  account_id: number | null
  group_id: number | null
  model: string | null
  request_id: string | null
  client_request_id: string | null
  gateway_request_id: string | null
  upstream_request_id: string | null
  stream: boolean
  request_type: number
  timing: RequestTiming
}

export interface RequestTimingSlowTopResponse {
  items: RequestTimingDetail[]
  sort: SlowTimingSortKey
  start_time: string
  end_time: string
  /** 有界额外计数（root 新增，可缺）：命中行数与候选元信息，避免全窗 COUNT */
  quality?: TimingQuality | null
  /** 与趋势卡共享的聚合健康（root 新增，可缺） */
  aggregation?: TimingAggregation | null
}

export type SlowTimingSortKey = 'upload_ms' | 'upstream_ttft_ms' | 'real_ttfb_ms'

const num = (value: unknown): number | null => {
  if (value === null || value === undefined || value === '') return null
  const n = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(n) ? n : null
}

const bool = (value: unknown, fallback = false): boolean =>
  typeof value === 'boolean' ? value : fallback

const status = (value: unknown): RequestTimingStatus => {
  switch (value) {
    case 'complete':
    case 'partial':
    case 'unavailable':
    case 'invalid':
      return value
    default:
      return 'unavailable'
  }
}

/**
 * 归一后端 JSON。返回 null 表示"该行没有可展示的耗时数据"：
 * 调用方应展示不可用状态，而不是渲染 0 或半截瀑布。
 */
export const normalizeRequestTiming = (input: unknown): RequestTiming | null => {
  if (!input || typeof input !== 'object') return null
  const raw = input as Record<string, unknown>
  const timingStatus = status(raw.timing_status)
  const hasAny =
    num(raw.gateway_latency_ms) !== null ||
    num(raw.duration_ms) !== null ||
    num(raw.first_token_ms) !== null ||
    num(raw.upload_ms) !== null ||
    num(raw.stream_ms) !== null ||
    num(raw.real_ttfb_ms) !== null
  if (!hasAny && timingStatus === 'complete') return null
  if (!hasAny && timingStatus === 'unavailable' && raw.timing_method == null) {
    // 旧服务未实现该字段：仍返回对象但全部为 null，由 UI 显示"不可用"。
    return {
      gateway_latency_ms: null,
      body_bytes: null,
      duration_ms: null,
      first_token_ms: null,
      upload_ms: null,
      upstream_ttft_ms: null,
      stream_ms: null,
      real_ttfb_ms: null,
      upload_kbps: null,
      bytes_per_token: null,
      tps_total: null,
      tps_stream: null,
      raw_upload_ms: null,
      timing_residual_ms: null,
      ctx_tokens: null,
      timing_status: 'unavailable',
      timing_method: null,
      reason: typeof raw.reason === 'string' ? raw.reason : null,
      is_http_stream: bool(raw.is_http_stream),
      is_complete_timing: false,
      is_valid_upload: false,
      is_valid_ttft: false,
      is_valid_real: false,
      is_valid_rate: false,
      is_valid_body: false,
    }
  }

  return {
    gateway_latency_ms: num(raw.gateway_latency_ms),
    body_bytes: num(raw.body_bytes),
    duration_ms: num(raw.duration_ms),
    first_token_ms: num(raw.first_token_ms),
    upload_ms: num(raw.upload_ms),
    upstream_ttft_ms: num(raw.upstream_ttft_ms),
    stream_ms: num(raw.stream_ms),
    real_ttfb_ms: num(raw.real_ttfb_ms),
    upload_kbps: num(raw.upload_kbps),
    bytes_per_token: num(raw.bytes_per_token),
    tps_total: num(raw.tps_total),
    tps_stream: num(raw.tps_stream),
    raw_upload_ms: num(raw.raw_upload_ms),
    timing_residual_ms: num(raw.timing_residual_ms),
    ctx_tokens: num(raw.ctx_tokens),
    timing_status: timingStatus,
    timing_method: typeof raw.timing_method === 'string' ? raw.timing_method : null,
    reason: typeof raw.reason === 'string' ? raw.reason : null,
    is_http_stream: bool(raw.is_http_stream),
    is_complete_timing: bool(raw.is_complete_timing, timingStatus === 'complete'),
    is_valid_upload: bool(raw.is_valid_upload),
    is_valid_ttft: bool(raw.is_valid_ttft),
    is_valid_real: bool(raw.is_valid_real),
    is_valid_rate: bool(raw.is_valid_rate),
    is_valid_body: bool(raw.is_valid_body),
  }
}

/** 三段条几何：三段齐全且 total>0 才可绘制；否则返回 null（不可用分支）。 */
export const timingSegments = (
  timing: RequestTiming | null
): { upload: number; upstreamTtft: number; stream: number; total: number } | null => {
  if (!timing) return null
  const { upload_ms: upload, upstream_ttft_ms: upstream, stream_ms: stream } = timing
  if (upload === null || upstream === null || stream === null) return null
  if (upload < 0 || upstream < 0 || stream < 0) return null
  const total = upload + upstream + stream
  if (!(total > 0)) return null
  return { upload, upstreamTtft: upstream, stream, total }
}
