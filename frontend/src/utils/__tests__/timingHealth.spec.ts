import { describe, expect, it } from 'vitest'
import {
  TIMING_REASONS,
  deriveTimingStatus,
  TIMING_REASON_TONE,
  isTimingReason,
  orderTimingReasons,
  sampleHintParams,
  timingReasonBadgeClass,
  timingReasonHintKey,
  timingReasonLabelKey,
} from '../timingHealth'

describe('timingHealth reason taxonomy', () => {
  it('exposes exactly the five operator-facing reasons the backend reports', () => {
    expect([...TIMING_REASONS]).toEqual([
      'aggregation_error',
      'unfinalized',
      'fields_missing',
      'no_requests',
      'insufficient_samples',
    ])
  })

  it('keeps reasons in a stable, actionable order and de-duplicates', () => {
    // 乱序 + 重复 + 未知 code：未知丢弃，已知按「错误 → 等待 → 形态 → 样本」排序
    expect(
      orderTimingReasons([
        'insufficient_samples',
        'no_requests',
        'unfinalized',
        'aggregation_error',
        'no_requests',
        null,
        undefined,
        'brand_new_reason',
      ])
    ).toEqual(['aggregation_error', 'unfinalized', 'no_requests', 'insufficient_samples'])
  })

  it('marks aggregation failures as errors, waiting/shape issues as warnings, sample facts as info', () => {
    expect(TIMING_REASON_TONE.aggregation_error).toBe('error')
    expect(TIMING_REASON_TONE.unfinalized).toBe('warn')
    expect(TIMING_REASON_TONE.fields_missing).toBe('warn')
    expect(TIMING_REASON_TONE.no_requests).toBe('info')
    expect(TIMING_REASON_TONE.insufficient_samples).toBe('info')

    expect(timingReasonBadgeClass('aggregation_error')).toContain('rose')
    expect(timingReasonBadgeClass('unfinalized')).toContain('amber')
    expect(timingReasonBadgeClass('no_requests')).not.toContain('rose')
  })

  it('points every reason at an i18n label and hint', () => {
    for (const reason of TIMING_REASONS) {
      expect(timingReasonLabelKey(reason)).toBe(`admin.ops.requestTiming.states.${reason}`)
      expect(timingReasonHintKey(reason)).toBe(`admin.ops.requestTiming.states.hints.${reason}`)
    }
  })

  it('rejects unknown codes instead of leaking raw backend strings', () => {
    expect(isTimingReason('no_requests')).toBe(true)
    expect(isTimingReason('NO_REQUESTS')).toBe(false)
    expect(isTimingReason(undefined)).toBe(false)
    expect(orderTimingReasons(['something_else'])).toEqual([])
  })

  it('maps per-chart sample facts to template params', () => {
    expect(
      sampleHintParams({ actualMaxSamples: 3, bucketsWithValue: 2, bucketCount: 12, minSamples: 5 })
    ).toEqual({ actual: 3, min: 5, withValue: 2, total: 12 })
  })
})

describe('deriveTimingStatus', () => {
  const aggregation = (over: Partial<import('@/types/requestTiming').TimingAggregation>) => ({
    status: 'healthy' as const,
    server_time: '2026-05-01T10:03:00Z',
    target_through: '2026-05-01T10:01:00Z',
    finalized_through: '2026-05-01T10:01:00Z',
    lag_seconds: 0,
    last_run_at: '2026-05-01T10:02:30Z',
    last_success_at: '2026-05-01T10:02:30Z',
    last_error_at: null,
    last_error: null,
    pending_minutes: 0,
    oldest_pending_at: null,
    ...over,
  })

  it('maps aggregation failures and lag to aggregation_error, never to no_requests', () => {
    for (const status of ['error', 'stale', 'not_started'] as const) {
      const result = deriveTimingStatus(aggregation({ status, lag_seconds: 7300, pending_minutes: 40 }), {
        candidateCount: 0,
        metricN: 0,
        windowFinalized: true,
      })
      expect(result.reasons).toEqual(['aggregation_error'])
      expect(result.lagSeconds).toBe(7300)
      expect(result.pendingMinutes).toBe(40)
    }
  })

  it('treats catching_up and pending buckets as unfinalized and suppresses no_requests', () => {
    const catchingUp = deriveTimingStatus(aggregation({ status: 'catching_up' }), {
      candidateCount: 0,
      metricN: 0,
      windowFinalized: true,
    })
    expect(catchingUp.reasons).toEqual(['unfinalized'])

    const pendingBucket = deriveTimingStatus(aggregation({ status: 'healthy' }), {
      candidateCount: 0,
      metricN: 0,
      hasPendingBuckets: true,
      windowFinalized: true,
    })
    // 未定稿窗口不允许被判成「没有请求」
    expect(pendingBucket.reasons).toEqual(['unfinalized'])
  })

  it('reports no_requests only for a finalized window with zero candidates', () => {
    const result = deriveTimingStatus(aggregation({ status: 'healthy' }), {
      candidateCount: 0,
      metricN: 0,
      windowFinalized: true,
    })
    expect(result.reasons).toEqual(['no_requests'])
  })

  it('keeps the cause unknown when the window state is not reported at all', () => {
    const result = deriveTimingStatus(aggregation({ status: 'healthy' }), {
      candidateCount: 0,
      metricN: 0,
    })
    expect(result.reasons).toEqual([])
  })

  it('distinguishes fields_missing from insufficient_samples with actual counts', () => {
    const missing = deriveTimingStatus(aggregation({}), { candidateCount: 12, metricN: 0, windowFinalized: true })
    expect(missing.reasons).toEqual(['fields_missing'])
    expect(missing.candidateCount).toBe(12)
    expect(missing.metricN).toBe(0)

    // 12 个候选里只有 3 条带值：既是字段缺失（3/12），也低于最小样本 5 —— 两条都要出现
    const thin = deriveTimingStatus(aggregation({}), { candidateCount: 12, metricN: 3, windowFinalized: true })
    expect(thin.reasons).toEqual(['fields_missing', 'insufficient_samples'])
    expect(thin.minSamples).toBe(5)

    const covered = deriveTimingStatus(aggregation({}), { candidateCount: 12, metricN: 12, windowFinalized: true })
    expect(covered.reasons).toEqual([])
  })

  it('judges sample sufficiency per bucket, not by the window sum', () => {
    // 60 个桶各 1 条：Σn=60 看似充足，但每桶都 <5，图上依然画不出线
    const result = deriveTimingStatus(aggregation({}), {
      candidateCount: 60,
      metricN: 60,
      windowFinalized: true,
      drawableBuckets: 0,
      totalBuckets: 60,
      maxBucketN: 1,
    })
    expect(result.reasons).toEqual(['insufficient_samples'])
    expect(result.drawableBuckets).toBe(0)
    expect(result.maxBucketN).toBe(1)

    const drawable = deriveTimingStatus(aggregation({}), {
      candidateCount: 60,
      metricN: 60,
      windowFinalized: true,
      drawableBuckets: 2,
      totalBuckets: 60,
      maxBucketN: 40,
    })
    expect(drawable.reasons).toEqual([])
  })

  it('treats an explicitly unfinished window as unfinalized even without pending buckets', () => {
    const result = deriveTimingStatus(aggregation({ status: 'healthy' }), {
      candidateCount: 0,
      metricN: 0,
      windowFinalized: false,
    })
    expect(result.reasons).toEqual(['unfinalized'])
  })

  it('never claims no_requests or unfinalized when the window state is unknown', () => {
    const result = deriveTimingStatus(aggregation({ status: 'healthy' }), {
      candidateCount: 0,
      metricN: 0,
      windowFinalized: null,
    })
    expect(result.reasons).toEqual([])
  })

  it('orders aggregation failure before sample-shape causes', () => {
    const result = deriveTimingStatus(aggregation({ status: 'error' }), {
      candidateCount: 40,
      metricN: 2,
      windowFinalized: true,
    })
    expect(result.reasons).toEqual(['aggregation_error', 'fields_missing', 'insufficient_samples'])
  })
})
