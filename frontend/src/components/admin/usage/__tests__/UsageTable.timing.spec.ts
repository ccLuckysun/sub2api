import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const ipGeoMocks = vi.hoisted(() => ({
  getEntry: vi.fn(() => ({ status: 'idle' as const })),
  fetchOne: vi.fn(),
  fetchBatch: vi.fn(),
}))

const appStoreMocks = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/utils/ipGeoLookup', () => ipGeoMocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => appStoreMocks }))
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

import UsageTable from '../UsageTable.vue'

const TIMING_CELL_KEYS = ['upload_ms', 'upload_rate', 'body_bytes', 'upstream_ttft_ms', 'stream_ms', 'real_ttfb_ms']

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row-id="row.id">
        <slot name="cell-latency" :row="row" />
        <slot name="cell-upload_ms" :row="row" />
        <slot name="cell-upload_rate" :row="row" />
        <slot name="cell-body_bytes" :row="row" />
        <slot name="cell-upstream_ttft_ms" :row="row" />
        <slot name="cell-stream_ms" :row="row" />
        <slot name="cell-real_ttfb_ms" :row="row" />
      </div>
    </div>
  `,
}

const timingPayload = {
  gateway_latency_ms: 28_894,
  body_bytes: 6 * 1024 * 1024,
  duration_ms: 14_069,
  first_token_ms: 13_400,
  upload_ms: 8_000,
  upstream_ttft_ms: 13_400,
  stream_ms: 669,
  real_ttfb_ms: 21_400,
  upload_kbps: 100,
  bytes_per_token: 43.14,
  tps_total: 50,
  tps_stream: 100,
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

const legacyRow = {
  id: 1,
  request_id: 'req-legacy',
  model: 'gpt-5',
  output_tokens: 1_000,
  duration_ms: 20_000,
  first_token_ms: 10_000,
  billing_mode: 'token',
  image_count: 0,
}

const timingRow = {
  ...legacyRow,
  id: 2,
  request_id: 'req-timing',
  timing: timingPayload,
}

const mountTable = (data: unknown[], columns: { key: string; label: string; sortable?: boolean }[]) =>
  mount(UsageTable, {
    props: { data, loading: false, columns },
    global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
  })

const cellColumns = TIMING_CELL_KEYS.map((key) => ({ key, label: key, sortable: false }))

describe('UsageTable timing columns', () => {
  it('renders upload, rate, body and TTFB cells from the timing payload', () => {
    const wrapper = mountTable([timingRow], cellColumns)
    expect(wrapper.get('[data-row-id="2"] [data-testid="upload-ms-2"]').text()).toBe('8.00s')
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('100 KiB/s')
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('6.00 MiB')
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('13.4s')
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('669ms')
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('21.4s')
  })

  it('marks >5MiB bodies and upload severity, and emits timingClick on click', async () => {
    const wrapper = mountTable([timingRow], cellColumns)
    expect(wrapper.get('[data-row-id="2"]').text()).toContain('>5MiB')
    const button = wrapper.get('[data-testid="upload-ms-2"]')
    expect(button.classes()).toContain('text-amber-600')
    await button.trigger('click')
    expect(wrapper.emitted('timingClick')?.[0]?.[0]).toMatchObject({ id: 2, request_id: 'req-timing' })
  })

  it('shows a degraded marker instead of pretending the value is exact', () => {
    const partial = {
      ...timingRow,
      id: 3,
      timing: {
        ...timingPayload,
        timing_status: 'partial',
        is_complete_timing: false,
        is_valid_upload: false,
        is_valid_rate: false,
      },
    }
    const wrapper = mountTable([partial], cellColumns)
    const cell = wrapper.get('[data-row-id="3"]')
    expect(cell.find('[title="usage.timing.degradedHint"]').exists()).toBe(true)
  })

  it('renders an em dash rather than zero when timing is missing', () => {
    const wrapper = mountTable([legacyRow], cellColumns)
    const cell = wrapper.get('[data-row-id="1"]')
    expect(cell.text()).toContain('—')
    expect(cell.text()).not.toContain('0ms')
  })

  it('prefers the dual TPS values and falls back to the legacy full-duration rate', () => {
    const withTiming = mountTable([timingRow], [{ key: 'latency', label: 'Latency' }])
    expect(withTiming.get('[data-testid="output-tps"]').text()).toContain('50.0 tok/s')
    expect(withTiming.get('[data-testid="output-tps"]').text()).toContain('100.0 tok/s')
    expect(withTiming.text()).toContain('usage.timing.tpsDualLabel')

    const withoutTiming = mountTable([legacyRow], [{ key: 'latency', label: 'Latency' }])
    expect(withoutTiming.get('[data-testid="output-tps"]').text()).toBe('50.0 tok/s')
    expect(withoutTiming.text()).toContain('usage.outputTps')
  })
})
