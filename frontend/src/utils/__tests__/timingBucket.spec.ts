import { describe, expect, it } from 'vitest'
import {
  TIMING_CHART_MIN_SAMPLES,
  TIMING_MAX_POINTS,
  boundsMidpoint,
  bytesToMiB,
  customRangeMinutes,
  pickTimingBucketSeconds,
  timingWindowParams,
  toChartSeries,
} from '../timingBucket'

describe('pickTimingBucketSeconds', () => {
  it('keeps <= 720 points for every supported range', () => {
    const ranges: [string, number][] = [['5m', 5], ['30m', 30], ['1h', 60], ['6h', 360], ['24h', 1440]]
    for (const [range, span] of ranges) {
      const bucket = pickTimingBucketSeconds(range)
      expect([60, 300, 900]).toContain(bucket)
      expect(Math.ceil((span * 60) / bucket)).toBeLessThanOrEqual(TIMING_MAX_POINTS)
    }
    const week = pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-08T00:00:00Z')
    expect(week).toBe(900)
    expect(Math.ceil((7 * 24 * 60 * 60) / week)).toBeLessThanOrEqual(TIMING_MAX_POINTS)
  })

  it('never picks 300s beyond 24h even when the point budget would allow it', () => {
    // Backend rejects >24h with 300s buckets; 30h/60h must fall back to 900s.
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-02T06:00:00Z')).toBe(900)
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-03T12:00:00Z')).toBe(900)
    expect(pickTimingBucketSeconds('7d')).toBe(900)
    expect(pickTimingBucketSeconds('24h')).toBe(300)
  })

  it('rounds custom spans outward on both ends', () => {
    // 00:00:30 → 01:00:30 is 60min, but the covered buckets are 00:00..01:01 = 61.
    expect(customRangeMinutes('2026-01-01T00:00:30Z', '2026-01-01T01:00:30Z')).toBe(61)
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:30Z', '2026-01-01T01:00:30Z')).toBe(60)
    expect(customRangeMinutes('2026-01-01T00:00:00Z', '2026-01-01T00:00:30Z')).toBe(1)
  })

  it('uses the finest bucket that still fits 720 points', () => {
    expect(pickTimingBucketSeconds('5m')).toBe(60)
    expect(pickTimingBucketSeconds('1h')).toBe(60)
    expect(pickTimingBucketSeconds('6h')).toBe(60)
    expect(pickTimingBucketSeconds('24h')).toBe(300)
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-01T12:00:00Z')).toBe(60)
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-01T13:00:00Z')).toBe(300)
    expect(pickTimingBucketSeconds('custom', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')).toBe(300)
  })

  it('prefers the custom range when it is valid', () => {
    expect(pickTimingBucketSeconds('24h', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z')).toBe(60)
    expect(customRangeMinutes('2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z')).toBe(60)
    expect(customRangeMinutes('2026-01-01T01:00:00Z', '2026-01-01T00:00:00Z')).toBeNull()
    expect(customRangeMinutes(null, null)).toBeNull()
  })
})

describe('toChartSeries', () => {
  const groupPoints = [
    { bucket_start: 'a', n: 4, p95: 100 },
    { bucket_start: 'b', n: 5, p95: 200 },
    { bucket_start: 'c', n: 50, p95: null },
    { bucket_start: 'd', n: 50, p95: 400 },
  ]

  it('breaks the line below the chart sample floor and never fills zeros', () => {
    const values = toChartSeries(groupPoints, (point) => point.p95)
    expect(values).toEqual([null, 200, null, 400])
    expect(TIMING_CHART_MIN_SAMPLES).toBe(5)
  })

  it('reads the per-field sample count for trend points', () => {
    const trendPoints = [
      { bucket_start: 'a', upload_ms: { n: 3 }, upload_kbps: { n: 30 } },
      { bucket_start: 'b', upload_ms: { n: 30 }, upload_kbps: { n: 3 } },
    ]
    const upload = toChartSeries(
      trendPoints,
      (point) => point.upload_ms.n * 10,
      (point) => point.upload_ms.n
    )
    const rate = toChartSeries(
      trendPoints,
      (point) => point.upload_kbps.n * 10,
      (point) => point.upload_kbps.n
    )
    expect(upload).toEqual([null, 300])
    expect(rate).toEqual([300, null])
  })

  it('drops non-finite values without inventing a number', () => {
    const values = toChartSeries(
      [{ bucket_start: 'a', n: 10, p95: Number.NaN }],
      (point) => point.p95
    )
    expect(values).toEqual([null])
  })
})

describe('chart helpers', () => {
  it('converts bytes to MiB and keeps NULL as NULL', () => {
    expect(bytesToMiB(5 * 1024 * 1024)).toBe(5)
    expect(bytesToMiB(null)).toBeNull()
  })

  it('uses the bounds midpoint only when the upper bound exists', () => {
    expect(boundsMidpoint({ lo: 100, hi: 200 })).toBe(150)
    // Overflow buckets have no upper bound: drawing lo would fake a real value.
    expect(boundsMidpoint({ lo: 100, hi: null })).toBeNull()
    expect(boundsMidpoint(null)).toBeNull()
  })
})

describe('timingWindowParams', () => {
  const week = ['2026-01-01T00:00:00Z', '2026-01-08T00:00:00Z'] as const

  it('ignores stale custom dates once a preset range is selected', () => {
    const params = timingWindowParams('1h', week[0], week[1])
    expect(params.bucket_seconds).toBe(60)
    expect(params.start_time).toBeUndefined()
    expect(params.end_time).toBeUndefined()
  })

  it('uses the preset span for the bucket, not the stale custom span', () => {
    expect(timingWindowParams('24h', week[0], week[1]).bucket_seconds).toBe(300)
    expect(timingWindowParams('7d', week[0], week[1]).bucket_seconds).toBe(900)
  })

  it('passes absolute times and the coarse bucket for a custom week', () => {
    const params = timingWindowParams('custom', week[0], week[1])
    expect(params.bucket_seconds).toBe(900)
    expect(params.start_time).toBe(week[0])
    expect(params.end_time).toBe(week[1])
  })

  it('omits absolute times when a custom range is incomplete', () => {
    const params = timingWindowParams('custom', week[0], null)
    expect(params.start_time).toBeUndefined()
    expect(params.end_time).toBeUndefined()
  })
})
