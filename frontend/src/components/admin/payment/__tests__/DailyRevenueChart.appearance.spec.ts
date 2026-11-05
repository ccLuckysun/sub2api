import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { initializeAppearance, useAppearance } from '@/composables/useAppearance'
import { DEFAULT_APPEARANCE } from '@/types/appearance'
import DailyRevenueChart from '../DailyRevenueChart.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options'],
    template: '<div class="revenue-chart" />',
  },
}))

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('style')
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  initializeAppearance({ ...DEFAULT_APPEARANCE, theme: 'light' })
})

describe('DailyRevenueChart appearance', () => {
  it('updates axes, legend, tooltip and motion without altering payment datasets', async () => {
    const wrapper = mount(DailyRevenueChart, {
      props: { data: [{ date: '2026-11-01', amount: { USD: 12, CNY: 24 }, count: 3 }] },
    })
    const chart = wrapper.findComponent({ name: 'Line' })
    const initialData = structuredClone(chart.props('data'))
    expect(chart.props('options').plugins.tooltip).toMatchObject({
      backgroundColor: '#FFFFFF', bodyColor: '#202126', borderWidth: 1,
    })
    useAppearance().setPreferences({ theme: 'dark', accent: 'pink', motion: 'off' })
    await nextTick()
    const options = chart.props('options')
    expect(options.plugins.tooltip).toMatchObject({
      backgroundColor: '#111216', bodyColor: '#ECE8E1', borderWidth: 1,
    })
    expect(options.plugins.legend.labels.color).toBe('#ECE8E1')
    expect(options.scales.x.ticks.color).toBe('#A6A19A')
    expect(options.scales.y.grid.color).toBe('#33353D')
    expect(options.scales.y1.position).toBe('right')
    expect(options.animation).toBe(false)
    expect(chart.props('data')).toEqual(initialData)
    wrapper.unmount()
  })

  it('retains the no-data state rather than inventing revenue', () => {
    const wrapper = mount(DailyRevenueChart, { props: { data: [] } })
    expect(wrapper.findComponent({ name: 'Line' }).exists()).toBe(false)
    expect(wrapper.text()).toContain('payment.admin.noData')
    wrapper.unmount()
  })
})
