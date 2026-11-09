import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}|${JSON.stringify(params)}` : key,
    }),
  }
})

import RequestTimingBreakdown from '../RequestTimingBreakdown.vue'
import type { RequestTiming } from '@/types/requestTiming'

const timing: RequestTiming = {
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

const mountBreakdown = (props: Record<string, unknown>) =>
  mount(RequestTimingBreakdown, {
    props,
    global: { stubs: { HelpTooltip: true } },
  })

describe('RequestTimingBreakdown', () => {
  it('renders the three-segment split with the documented totals', () => {
    const wrapper = mountBreakdown({ timing })
    expect(wrapper.get('[data-testid="timing-status"]').text()).toBe('usage.timing.status.complete')
    expect(wrapper.get('[data-testid="timing-total"]').text()).toBe('28.89s')
    expect(wrapper.get('[data-testid="timing-real-ttfb"]').text()).toBe('28.23s')

    const bars = wrapper.findAll('[role="img"] > div')
    expect(bars).toHaveLength(3)
    const widths = bars.map((bar) => Number((bar.attributes('style') ?? '').replace(/[^\d.]/g, '')))
    expect(widths[0]).toBeGreaterThan(widths[1])
    expect(widths[1]).toBeGreaterThan(widths[2])
    expect(widths.reduce((sum, w) => sum + w, 0)).toBeCloseTo(100, 1)
  })

  it('keeps the known fields and lists reasons when the split is unavailable', () => {
    const wrapper = mountBreakdown({
      timing: {
        ...timing,
        stream_ms: null,
        real_ttfb_ms: null,
        timing_status: 'partial',
        is_complete_timing: false,
        reason: 'missing_first_token',
      },
    })
    expect(wrapper.get('[data-testid="timing-status"]').text()).toBe('usage.timing.status.partial')
    expect(wrapper.find('[role="img"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('usage.timing.unavailableTitle')
    expect(wrapper.text()).toContain('usage.timing.duration')
    expect(wrapper.text()).toContain('usage.timing.firstToken')
    expect(wrapper.text()).toContain('usage.timing.reason.missingFirstToken')
  })

  it('shows a loading skeleton instead of a zeroed bar', () => {
    const wrapper = mountBreakdown({ timing: null, loading: true })
    expect(wrapper.find('[role="img"]').exists()).toBe(false)
    expect(wrapper.find('.animate-pulse').exists()).toBe(true)
  })
})
