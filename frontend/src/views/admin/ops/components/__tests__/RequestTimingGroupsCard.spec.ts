import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

const { getRequestTimingGroups } = vi.hoisted(() => ({ getRequestTimingGroups: vi.fn() }))

vi.mock('@/api/admin/ops', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/ops')>()
  return { ...actual, getRequestTimingGroups }
})
vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options'],
    emits: ['update:modelValue'],
    template: '<select><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
}))
// chart.js is irrelevant here: the budget/error banners render instead of the chart.
vi.mock('vue-chartjs', () => ({ Line: { template: '<div data-testid="timing-groups-chart" />' } }))

import RequestTimingGroupsCard from '../RequestTimingGroupsCard.vue'

const mountCard = () =>
  mount(RequestTimingGroupsCard, {
    props: { timeRange: '1h' },
    global: { stubs: { HelpTooltip: true } },
  })

describe('RequestTimingGroupsCard budget handling', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows the budget card only for the documented budget code', async () => {
    getRequestTimingGroups.mockRejectedValue({
      status: 422,
      code: 'REQUEST_TIMING_BUDGET_EXCEEDED',
      message: 'too many rows',
    })
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-groups-budget"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('too many rows')
  })

  it('does not leak stale custom dates after switching back to a preset range', async () => {
    getRequestTimingGroups.mockResolvedValue({ mode: 'raw_exact', series: [], quality: null })
    mount(RequestTimingGroupsCard, {
      props: {
        timeRange: '24h',
        customStartTime: '2026-01-01T00:00:00Z',
        customEndTime: '2026-01-08T00:00:00Z',
      },
      global: { stubs: { HelpTooltip: true } },
    })
    await flushPromises()

    const params = getRequestTimingGroups.mock.calls.at(-1)?.[0]
    expect(params.start_time).toBeUndefined()
    expect(params.end_time).toBeUndefined()
    expect(params.bucket_seconds).toBe(300)
  })

  it('treats other 422 responses as ordinary errors', async () => {
    getRequestTimingGroups.mockRejectedValue({
      status: 422,
      code: 'VALIDATION_FAILED',
      message: 'entities must be account ids',
    })
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-groups-budget"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('entities must be account ids')
  })

  it('falls back to the load-failed message when the error carries none', async () => {
    getRequestTimingGroups.mockRejectedValue({ status: 500 })
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.find('[data-testid="timing-groups-budget"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.ops.requestTiming.loadFailed')
  })
})
