import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

/**
 * P1 回归：孤立有效桶必须可见。
 *
 * 之前两张图都是 `pointRadius: 0` + `spanGaps: false`：当一个窗口里只有一个 n>=5 的桶、
 * 前后都是 null 时，Chart.js 不画线也不画点，「有样本」被渲染成完全空白的图。
 * 现在有效点半径 2、null 点半径 0 —— 断线语义不变，孤立样本可见。
 */

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

const captured: { chartData: Record<string, unknown> }[] = []

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    setup(props: { data: Record<string, unknown> }) {
      captured.push({ chartData: props.data })
      return () => null
    },
  },
}))

const { getRequestTimingTrend, getRequestTimingGroups } = vi.hoisted(() => ({
  getRequestTimingTrend: vi.fn(),
  getRequestTimingGroups: vi.fn(),
}))

vi.mock('@/api/admin/ops', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/ops')>()
  return { ...actual, getRequestTimingTrend, getRequestTimingGroups }
})

vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options'],
    emits: ['update:modelValue'],
    template: '<select><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
}))

import RequestTimingTrendCard from '../RequestTimingTrendCard.vue'
import RequestTimingGroupsCard from '../RequestTimingGroupsCard.vue'
import { TIMING_POINT_HOVER_RADIUS, TIMING_POINT_RADIUS, timingPointRadius } from '../timingChartPoints'

const quantile = (n: number, p50: number | null, extra: Record<string, number> = {}) => ({
  n,
  p10: p50,
  p50,
  p90: p50,
  p95: p50,
  p10_bounds: null,
  p50_bounds: null,
  p90_bounds: null,
  p95_bounds: null,
  ...extra,
})

const trendWith = (points: Record<string, unknown>[]) => ({
  mode: 'raw_exact',
  hist_version: null,
  bucket_seconds: 60,
  start_time: '2026-05-01T10:00:00Z',
  end_time: '2026-05-01T10:03:00Z',
  finalized_through: '2026-05-01T10:02:00Z',
  computed_at: '2026-05-01T10:03:10Z',
  provisional: false,
  degradation_reason: null,
  quality: {
    candidate_count: 12,
    complete_count: 10,
    invalid_count: 1,
    upload_count: 10,
    ttft_count: 10,
    real_count: 10,
    rate_count: 10,
    known_body_count: 10,
    large_body_count: 1,
    coverage_ratio: 0.83,
  },
  points,
})

const datasets = () => captured.flatMap((entry) => ((entry.chartData.datasets ?? []) as Record<string, unknown>[]))

beforeEach(() => {
  captured.length = 0
  vi.clearAllMocks()
})

describe('timingPointRadius', () => {
  it('keeps isolated samples visible and null points invisible', () => {
    expect(timingPointRadius({ raw: 12 })).toBe(TIMING_POINT_RADIUS)
    expect(TIMING_POINT_RADIUS).toBe(2)
    expect(timingPointRadius({ raw: 0 })).toBe(2)
    expect(timingPointRadius({ raw: null })).toBe(0)
    expect(timingPointRadius({ raw: undefined })).toBe(0)
    expect(timingPointRadius({})).toBe(0)
    expect(TIMING_POINT_HOVER_RADIUS).toBe(4)
  })
})

describe('trend card point visibility', () => {
  it('draws a point for a single isolated qualifying bucket and nulls below-threshold buckets', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendWith([
        { bucket_start: '2026-05-01T10:00:00Z', upload_ms: quantile(4, null), upload_kbps: quantile(4, null), body_bytes: quantile(4, null) },
        { bucket_start: '2026-05-01T10:01:00Z', upload_ms: quantile(11, 5677), upload_kbps: quantile(11, 35), body_bytes: quantile(11, 1.2) },
        { bucket_start: '2026-05-01T10:02:00Z', upload_ms: quantile(3, null), upload_kbps: quantile(3, null), body_bytes: quantile(3, null) },
      ])
    )
    mount(RequestTimingTrendCard, { props: { timeRange: '1h' }, global: { stubs: { HelpTooltip: true } } })
    await flushPromises()

    const sets = datasets()
    expect(sets.length).toBeGreaterThan(0)
    // 第一张图（upload）：只有中间桶有效 → 恰有一个非 null 值，其余保持 null（不连线、不插值）
    const upload = sets[0] as { data: (number | null)[]; pointRadius: unknown; pointHoverRadius: number; spanGaps: boolean }
    expect(upload.data).toEqual([null, 5677, null])
    expect(upload.spanGaps).toBe(false)
    expect(typeof upload.pointRadius).toBe('function')
    const radius = upload.pointRadius as (ctx: { raw?: unknown }) => number
    expect(radius({ raw: 5677 })).toBe(2)
    expect(radius({ raw: null })).toBe(0)
    expect(upload.pointHoverRadius).toBe(4)
  })
})

describe('groups card point visibility', () => {
  it('draws a point for an isolated p95 sample instead of hiding it', async () => {
    getRequestTimingGroups.mockResolvedValue({
      mode: 'raw_exact',
      dimension: 'account',
      bucket_seconds: 60,
      start_time: '2026-05-01T10:00:00Z',
      end_time: '2026-05-01T10:03:00Z',
      series: [
        {
          key: '3',
          label: 'acc-3',
          sample_count: 18,
          points: [
            // n=4 < 5：p95 有值也不许画（小样本抖动）
            { bucket_start: '2026-05-01T10:00:00Z', n: 4, p95: 900 },
            // n=5 恰好达标：孤立桶，必须画出 2px 的点
            { bucket_start: '2026-05-01T10:01:00Z', n: 5, p95: 412.5 },
            { bucket_start: '2026-05-01T10:02:00Z', n: 9, p95: 380 },
            { bucket_start: '2026-05-01T10:03:00Z', n: 0, p95: null },
          ],
        },
      ],
    })
    mount(RequestTimingGroupsCard, { props: { timeRange: '1h' }, global: { stubs: { HelpTooltip: true } } })
    await flushPromises()

    const set = datasets()[0] as { data: (number | null)[]; pointRadius: (ctx: { raw?: unknown }) => number; spanGaps: boolean }
    // 每桶按自己的 n 判定：n=4 → null（不画），n=5/9 → 画
    expect(set.data).toEqual([null, 412.5, 380, null])
    expect(set.spanGaps).toBe(false)
    expect(set.pointRadius({ raw: 412.5 })).toBe(2)
    expect(set.pointRadius({ raw: 380 })).toBe(2)
    expect(set.pointRadius({ raw: null })).toBe(0)
  })
})
