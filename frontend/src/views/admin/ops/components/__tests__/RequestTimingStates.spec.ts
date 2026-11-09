import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { TimingAggregation } from '@/types/requestTiming'

/**
 * repair 契约的状态呈现回归：
 * 空图必须说明「为什么」——no_requests / fields_missing / insufficient_samples / unfinalized /
 * aggregation_error 可并列，未定稿窗口永远不能显示成「没有请求」。
 */

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

vi.mock('vue-chartjs', () => ({ Line: { template: '<div data-testid="chart" />' } }))

const { getRequestTimingTrend, getRequestTimingGroups, listSlowTimingRequests } = vi.hoisted(() => ({
  getRequestTimingTrend: vi.fn(),
  getRequestTimingGroups: vi.fn(),
  listSlowTimingRequests: vi.fn(),
}))

vi.mock('@/api/admin/ops', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/ops')>()
  return { ...actual, getRequestTimingTrend, getRequestTimingGroups, listSlowTimingRequests }
})

vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options'],
    emits: ['update:modelValue'],
    template: '<select><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
}))

import RequestTimingTrendCard from '../RequestTimingTrendCard.vue'
import RequestTimingQualityCard from '../RequestTimingQualityCard.vue'
import RequestTimingGroupsCard from '../RequestTimingGroupsCard.vue'
import SlowRequestTopCard from '../SlowRequestTopCard.vue'

const aggregationOf = (over: Partial<TimingAggregation> = {}): TimingAggregation => ({
  status: 'healthy',
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

const quantiles = (n: number) => ({
  n,
  p10: null,
  p50: null,
  p90: null,
  p95: null,
  p10_bounds: null,
  p50_bounds: null,
  p90_bounds: null,
  p95_bounds: null,
})

const point = (bucket: string, n: number, over: Record<string, unknown> = {}) => ({
  bucket_start: bucket,
  finalized: true,
  pending: false,
  quality: {
    candidate_count: n,
    complete_count: n,
    invalid_count: 0,
    upload_count: n,
    ttft_count: n,
    real_count: n,
    rate_count: n,
    known_body_count: n,
    large_body_count: 0,
    coverage_ratio: n > 0 ? 1 : null,
  },
  upload_ms: quantiles(n),
  upload_kbps: quantiles(n),
  body_bytes: quantiles(n),
  real_ttfb_ms: quantiles(n),
  ...over,
})

const trendOf = (over: Record<string, unknown> = {}) => ({
  mode: 'raw_exact',
  hist_version: null,
  bucket_seconds: 60,
  start_time: '2026-05-01T10:00:00Z',
  end_time: '2026-05-01T10:02:00Z',
  finalized_through: '2026-05-01T10:01:00Z',
  computed_at: '2026-05-01T10:02:10Z',
  provisional: false,
  degradation_reason: null,
  aggregation: aggregationOf(),
  quality: {
    candidate_count: 0,
    complete_count: 0,
    invalid_count: 0,
    upload_count: 0,
    ttft_count: 0,
    real_count: 0,
    rate_count: 0,
    known_body_count: 0,
    large_body_count: 0,
    coverage_ratio: null,
  },
  points: [point('2026-05-01T10:00:00Z', 0), point('2026-05-01T10:01:00Z', 0)],
  ...over,
})

const mountTrend = () =>
  mount(RequestTimingTrendCard, { props: { timeRange: '1h' }, global: { stubs: { HelpTooltip: true } } })

describe('trend card empty-state diagnosis', () => {
  beforeEach(() => vi.clearAllMocks())

  it('blames aggregation when the watermark is hours behind and never claims "no requests"', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({ aggregation: aggregationOf({ status: 'stale', lag_seconds: 7300, pending_minutes: 40 }) })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-status-reason-aggregation_error"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-reason-no_requests"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="timing-status-lag"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-watermark"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-pending"]').exists()).toBe(true)
  })

  it('marks a finalized empty window as no_requests and shows the sample threshold', async () => {
    getRequestTimingTrend.mockResolvedValue(trendOf())
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-trend-reason-upload-no_requests"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-samples-upload"]').exists()).toBe(true)
  })

  it('explains candidate rows without metric values as fields_missing, not as no_requests', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({
        points: [point('2026-05-01T10:00:00Z', 0), point('2026-05-01T10:01:00Z', 0)],
        quality: {
          candidate_count: 30,
          complete_count: 0,
          invalid_count: 0,
          upload_count: 0,
          ttft_count: 0,
          real_count: 0,
          rate_count: 0,
          known_body_count: 0,
          large_body_count: 0,
          coverage_ratio: 0,
        },
      })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-trend-reason-upload-fields_missing"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-reason-upload-no_requests"]').exists()).toBe(false)
  })

  it('flags thin buckets as insufficient_samples and shows pending buckets separately', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({
        points: [
          point('2026-05-01T10:00:00Z', 3),
          point('2026-05-01T10:01:00Z', 0, { finalized: false, pending: true }),
        ],
        quality: {
          candidate_count: 3,
          complete_count: 3,
          invalid_count: 0,
          upload_count: 3,
          ttft_count: 3,
          real_count: 3,
          rate_count: 3,
          known_body_count: 3,
          large_body_count: 0,
          coverage_ratio: 1,
        },
      })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-status-reason-unfinalized"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-pending"]').exists()).toBe(true)
    // 3 个样本 < 5：空图上必须写出样本不足，而不是「暂无有效样本」
    expect(wrapper.find('[data-testid="timing-trend-reason-upload-insufficient_samples"]').exists()).toBe(true)
  })
})

describe('trend card sample-shape diagnosis (per bucket, compounding reasons)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows fields_missing and insufficient_samples together when only 3 of 12 candidates carry values', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({
        points: [point('2026-05-01T10:00:00Z', 3), point('2026-05-01T10:01:00Z', 0)],
        quality: {
          candidate_count: 12,
          complete_count: 3,
          invalid_count: 0,
          upload_count: 3,
          ttft_count: 3,
          real_count: 3,
          rate_count: 3,
          known_body_count: 3,
          large_body_count: 0,
          coverage_ratio: 0.25,
        },
      })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-trend-reason-upload-fields_missing"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-reason-upload-insufficient_samples"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-reason-upload-no_requests"]').exists()).toBe(false)
  })

  it('judges per-bucket samples so 60 buckets of 1 sample are not treated as plottable', async () => {
    const buckets = Array.from({ length: 60 }, (_, index) => {
      const minute = String(index % 60).padStart(2, '0')
      return point(`2026-05-01T10:${minute}:00Z`, 1)
    })
    getRequestTimingTrend.mockResolvedValue(
      trendOf({
        points: buckets,
        quality: {
          candidate_count: 60,
          complete_count: 60,
          invalid_count: 0,
          upload_count: 60,
          ttft_count: 60,
          real_count: 60,
          rate_count: 60,
          known_body_count: 60,
          large_body_count: 0,
          coverage_ratio: 1,
        },
      })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-trend-reason-upload-insufficient_samples"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-trend-reason-upload-no_requests"]').exists()).toBe(false)
    // 头部统计必须按桶报「可连图 N/总桶数」，而不是只报总量
    expect(wrapper.find('[data-testid="timing-trend-samples-upload"]').text()).toBe(
      'admin.ops.requestTiming.states.samplesBucketInline'
    )
  })

  it('marks a window with unfinalized buckets as unfinalized even when no bucket is pending', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({
        points: [
          point('2026-05-01T10:00:00Z', 0, { finalized: false, pending: false }),
          point('2026-05-01T10:01:00Z', 0, { finalized: false, pending: false }),
        ],
      })
    )
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-status-reason-unfinalized"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-reason-no_requests"]').exists()).toBe(false)
  })

  it('describes raw-mode emptiness as a raw observation, not a backend "no requests" verdict', async () => {
    getRequestTimingTrend.mockResolvedValue(trendOf({ mode: 'raw_exact' }))
    const wrapper = mountTrend()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-trend-empty-hint-upload"]').text()).toBe(
      'admin.ops.requestTiming.trend.emptyRaw'
    )
    // 仍然要带全局聚合延迟
    expect(wrapper.find('[data-testid="timing-trend-watermark"]').exists()).toBe(true)
  })
})

describe('quality card always exposes aggregation facts', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders the banner even when nothing is wrong, and reports the lag', async () => {
    getRequestTimingTrend.mockResolvedValue(
      trendOf({ aggregation: aggregationOf({ status: 'catching_up', lag_seconds: 420, pending_minutes: 7 }) })
    )
    const wrapper = mount(RequestTimingQualityCard, {
      props: { timeRange: '1h' },
      global: { stubs: { HelpTooltip: true } },
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-status-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-reason-unfinalized"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-lag"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-status-candidates"]').exists()).toBe(true)
  })
})

describe('groups and slow cards never assert "no requests"', () => {
  beforeEach(() => vi.clearAllMocks())

  it('groups empty state points at the filters and shared aggregation', async () => {
    getRequestTimingGroups.mockResolvedValue({
      mode: 'raw_exact',
      dimension: 'account',
      bucket_seconds: 60,
      start_time: '2026-05-01T10:00:00Z',
      end_time: '2026-05-01T10:02:00Z',
      quality: {
        candidate_count: 0,
        complete_count: 0,
        invalid_count: 0,
        upload_count: 0,
        ttft_count: 0,
        real_count: 0,
        rate_count: 0,
        known_body_count: 0,
        large_body_count: 0,
        coverage_ratio: null,
      },
      series: [],
    })
    const wrapper = mount(RequestTimingGroupsCard, {
      props: { timeRange: '1h', aggregation: aggregationOf({ status: 'catching_up' }) },
      global: { stubs: { HelpTooltip: true } },
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-groups-empty"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-groups-reason-unfinalized"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="timing-groups-reason-no_requests"]').exists()).toBe(false)
  })

  it('slow top says the filters matched nothing instead of blaming traffic', async () => {
    listSlowTimingRequests.mockResolvedValue({
      items: [],
      sort: 'upload_ms',
      start_time: '2026-05-01T10:00:00Z',
      end_time: '2026-05-01T10:02:00Z',
      quality: {
        candidate_count: 0,
        complete_count: 0,
        invalid_count: 0,
        upload_count: 0,
        ttft_count: 0,
        real_count: 0,
        rate_count: 0,
        known_body_count: 0,
        large_body_count: 0,
        coverage_ratio: null,
      },
    })
    const wrapper = mount(SlowRequestTopCard, {
      props: { timeRange: '1h' },
      global: { stubs: { HelpTooltip: true } },
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="slow-timing-empty"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="slow-timing-reason-no_requests"]').exists()).toBe(false)
  })
})
