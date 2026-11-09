import { describe, expect, it } from 'vitest'
import {
  BYTES_PER_TOKEN_HINT,
  LARGE_BODY_BYTES,
  formatBodySize,
  formatBytesExact,
  formatBytesPerToken,
  formatTimingMs,
  formatTimingSeconds,
  formatTokensPerSecond,
  formatUploadRate,
  isSlowTimingSortKey,
  isTimingDegraded,
  shouldHintLargeBytesPerToken,
  timingReasonLabelKey,
  uploadRateSeverity,
  uploadSeverity,
} from '../timingMetrics'
import { normalizeRequestTiming, timingSegments } from '@/types/requestTiming'

const completeTiming = {
  gateway_latency_ms: 28_894,
  body_bytes: 593_412,
  duration_ms: 14_069,
  first_token_ms: 13_400,
  upload_ms: 14_825,
  upstream_ttft_ms: 13_400,
  stream_ms: 669,
  real_ttfb_ms: 28_225,
  upload_kbps: 912.41,
  bytes_per_token: 43.14,
  tps_total: 71.07,
  tps_stream: 1494.77,
  raw_upload_ms: 14_825,
  timing_residual_ms: 0,
  ctx_tokens: 13_756,
  timing_status: 'complete',
  timing_method: 'legacy_difference_v1',
  reason: null,
  is_http_stream: true,
  is_complete_timing: true,
  is_valid_upload: true,
  is_valid_ttft: true,
  is_valid_real: true,
  is_valid_rate: true,
  is_valid_body: true,
}

describe('timingMetrics thresholds', () => {
  it('keeps upload severity strictly above the documented boundaries', () => {
    expect(uploadSeverity(5_000)).toBe('good')
    expect(uploadSeverity(5_001)).toBe('warn')
    expect(uploadSeverity(10_000)).toBe('warn')
    expect(uploadSeverity(10_001)).toBe('critical')
    expect(uploadSeverity(null)).toBeNull()
  })

  it('marks upload rate below 500 KiB/s as critical and never flags NULL', () => {
    expect(uploadRateSeverity(499.99)).toBe('critical')
    expect(uploadRateSeverity(500)).toBe('good')
    expect(uploadRateSeverity(null)).toBeNull()
  })
})

describe('timingMetrics formatting', () => {
  it('formats milliseconds, seconds and the fixed two-decimal breakpoint values', () => {
    expect(formatTimingMs(900)).toBe('900ms')
    expect(formatTimingMs(1_500)).toBe('1.50s')
    expect(formatTimingMs(13_400)).toBe('13.4s')
    expect(formatTimingMs(null)).toBe('—')
    expect(formatTimingSeconds(13_400)).toBe('13.40s')
    expect(formatTimingSeconds(1_425)).toBe('1.43s')
    expect(formatTimingSeconds(27_469)).toBe('27.47s')
  })

  it('uses MiB for body size, KiB/s for rate and B/token for density', () => {
    expect(formatBodySize(5 * 1024 * 1024)).toBe('5.00 MiB')
    expect(formatBodySize(2 * 1024)).toBe('2.0 KiB')
    expect(formatBodySize(512)).toBe('512 B')
    expect(formatBytesExact(593_412)).toBe('593,412 B')
    expect(formatUploadRate(912.41)).toBe('912 KiB/s')
    expect(formatUploadRate(12.34)).toBe('12.3 KiB/s')
    expect(formatUploadRate(1.234)).toBe('1.23 KiB/s')
    expect(formatTokensPerSecond(71.07)).toBe('71.1 tok/s')
    expect(formatBytesPerToken(43.1361)).toBe('43.14 B/token')
  })

  it('hints only when body-per-token exceeds the threshold', () => {
    expect(shouldHintLargeBytesPerToken(BYTES_PER_TOKEN_HINT)).toBe(false)
    expect(shouldHintLargeBytesPerToken(BYTES_PER_TOKEN_HINT + 1)).toBe(true)
    expect(shouldHintLargeBytesPerToken(null)).toBe(false)
  })

  it('keeps the large-body boundary at 5 MiB', () => {
    expect(LARGE_BODY_BYTES).toBe(5 * 1024 * 1024)
  })
})

describe('timingMetrics contracts shared with the backend', () => {
  it('whitelists exactly the three slow-timing sort keys', () => {
    expect(isSlowTimingSortKey('upload_ms')).toBe(true)
    expect(isSlowTimingSortKey('upstream_ttft_ms')).toBe(true)
    expect(isSlowTimingSortKey('real_ttfb_ms')).toBe(true)
    expect(isSlowTimingSortKey('created_at')).toBe(false)
  })

  it('maps every backend reason to an i18n key', () => {
    expect(timingReasonLabelKey('missing_body')).toBe('usage.timing.reason.missingBody')
    expect(timingReasonLabelKey('not_http_stream')).toBe('usage.timing.reason.notHttpStream')
    expect(timingReasonLabelKey('something_new')).toBeNull()
  })
})

describe('normalizeRequestTiming', () => {
  it('keeps the contract shape and derived values', () => {
    const timing = normalizeRequestTiming(completeTiming)
    expect(timing).not.toBeNull()
    expect(timing!.upload_ms).toBe(14_825)
    expect(timing!.timing_status).toBe('complete')
    expect(timing!.is_complete_timing).toBe(true)
    expect(isTimingDegraded(timing)).toBe(false)
    expect(timingSegments(timing)).toEqual({
      upload: 14_825,
      upstreamTtft: 13_400,
      stream: 669,
      total: 28_894,
    })
  })

  it('returns null only when the payload truly carries no timing', () => {
    expect(normalizeRequestTiming(null)).toBeNull()
    expect(normalizeRequestTiming(undefined)).toBeNull()
    // 空对象相当于"旧服务/未实现"的响应：返回不可用对象而不是 null，避免把缺失伪装成完整。
    expect(normalizeRequestTiming({})?.timing_status).toBe('unavailable')
  })

  it('never fabricates values for legacy payloads that omit the field', () => {
    const timing = normalizeRequestTiming({ timing_status: 'unavailable' })
    expect(timing).not.toBeNull()
    expect(timing!.upload_ms).toBeNull()
    expect(timing!.tps_total).toBeNull()
    expect(timing!.timing_status).toBe('unavailable')
    expect(timingSegments(timing)).toBeNull()
    expect(isTimingDegraded(timing)).toBe(true)
  })

  it('treats a partial row as degraded even when some segments exist', () => {
    const timing = normalizeRequestTiming({
      ...completeTiming,
      timing_status: 'partial',
      stream_ms: null,
      is_complete_timing: false,
      is_valid_real: false,
      reason: 'missing_first_token',
    })
    expect(isTimingDegraded(timing)).toBe(true)
    expect(timingSegments(timing)).toBeNull()
    expect(timing!.reason).toBe('missing_first_token')
  })
})
