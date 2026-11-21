import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsRealtimeRequestsCard from '../OpsRealtimeRequestsCard.vue'
import OpsRealtimeRequestDetailModal from '../OpsRealtimeRequestDetailModal.vue'
import { getFailureReasonLabel, isRequestSuccess } from '../../utils/realtimeRequestFormatters'
import type { OpsRealtimeRequestItem, OpsRealtimeRequestsResponse } from '@/api/admin/ops'
import enLocale from '@/i18n/locales/en'
import zhLocale from '@/i18n/locales/zh'

const mockGetRealtimeRequests = vi.fn()
const mockCopyToClipboard = vi.fn()
const mockShowWarning = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRealtimeRequests: (...args: any[]) => mockGetRealtimeRequests(...args)
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: (...args: any[]) => mockShowWarning(...args)
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: (...args: any[]) => mockCopyToClipboard(...args)
  })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, any>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

vi.mock('@vueuse/core', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@vueuse/core')>()
  return {
    ...actual,
    useMediaQuery: () => ({ value: true })
  }
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: {
      type: [String, Number],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <select
      class="select-stub"
      :value="modelValue"
      @change="$emit('update:modelValue', typeof modelValue === 'number' ? Number($event.target.value) : $event.target.value)"
    >
      <option v-for="opt in options" :key="String(opt.value)" :value="opt.value">{{ opt.label }}</option>
    </select>
  `
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialogStub',
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' }
  },
  emits: ['close'],
  template: `
    <div v-if="show" class="base-dialog-stub" data-testid="detail-modal">
      <div class="dialog-title">{{ title }}</div>
      <slot />
    </div>
  `
})

const IconStub = defineComponent({
  name: 'IconStub',
  props: { name: { type: String, default: '' } },
  template: '<span class="icon-stub" :data-icon="name" />'
})

const sampleItems: OpsRealtimeRequestItem[] = [
  {
    observed_at: '2026-11-21T06:10:00.123Z',
    request_id: 'req-alpha-001',
    client_request_id: 'cli-001',
    user_id: 10,
    user_email: 'alice@example.com',
    api_key_id: 20,
    api_key_name: 'Prod Key',
    api_key_masked: 'sk-***1234',
    account_id: 30,
    account_name: 'claude-prod-1',
    group_id: 2,
    group_name: 'primary-group',
    platform: 'anthropic',
    model: 'claude-sonnet-4-5',
    endpoint_class: 'messages',
    endpoint: '/v1/messages',
    result: 'success',
    http_status: 200,
    failure_reason: null,
    attempt_count: 1,
    failed_attempts: 0,
    node: 'gw-node-1',
    body_read_ms: 12,
    wait_ms: 8,
    gateway_ms: 15,
    upstream_ms: 820,
    headers_end_ms: 610,
    response_ms: 340,
    finish_ms: 5,
    total_ms: 1200,
    bytes_in_declared: 2048,
    bytes_in_wire: 2048,
    bytes_in_decoded: 2048,
    bytes_up_req_total: 2300,
    bytes_resp_upstream: 8192,
    bytes_resp_downstream: 8192,
    bytes_in_wire_status: 'complete',
    bytes_up_req_total_status: 'complete',
    bytes_resp_upstream_status: 'partial',
    bytes_resp_downstream_status: 'unknown'
  },
  {
    observed_at: '2026-11-21T06:10:05.456Z',
    request_id: 'req-beta-002',
    user_id: 11,
    user_email: 'bob@example.com',
    api_key_name: 'Dev Key',
    api_key_masked: 'sk-***9999',
    account_name: 'openai-acc-2',
    platform: 'openai',
    model: 'gpt-5',
    endpoint: '/v1/responses',
    result: 'failure',
    http_status: 504,
    failure_reason: 'upstream_timeout',
    attempt_count: 2,
    failed_attempts: 2,
    node: 'gw-node-2',
    body_read_ms: 20,
    wait_ms: 100,
    gateway_ms: 30,
    upstream_ms: 3500,
    headers_end_ms: null,
    response_ms: 0,
    finish_ms: 10,
    total_ms: 3660,
    bytes_in_wire: 512,
    bytes_in_wire_status: 'complete'
  }
]

const enabledResponse: OpsRealtimeRequestsResponse = {
  enabled: true,
  buffer_size: 2000,
  ttl_hours: 24,
  total_in_buffer: 128,
  summary: {
    total: 2,
    success: 1,
    failure: 1,
    success_rate: 50,
    avg_total_ms: 2430,
    slow_count: 1
  },
  items: sampleItems,
  models: ['claude-sonnet-4-5', 'gpt-5'],
  timestamp: '2026-11-21T06:10:06Z'
}

describe('OpsRealtimeRequestsCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    mockCopyToClipboard.mockResolvedValue(true)
    mockGetRealtimeRequests.mockResolvedValue(enabledResponse)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  const mountCard = () =>
    mount(OpsRealtimeRequestsCard, {
      global: {
        stubs: {
          Select: SelectStub,
          BaseDialog: BaseDialogStub,
          Icon: IconStub
        }
      }
    })

  it('renders buffer status, summary metrics, 6-phase legend, and request rows', async () => {
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.attributes('id')).toBe('realtime-requests')
    expect(mockGetRealtimeRequests).toHaveBeenCalledTimes(1)
    expect(mockGetRealtimeRequests).toHaveBeenCalledWith(
      { limit: 100, result: 'all' },
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )

    const text = wrapper.text()
    expect(text).toContain('admin.ops.realtimeRequests.bufferStatus:{"current":128,"capacity":2000,"ttl":24}')
    expect(text).toContain('50.0%')
    expect(text).toContain('2.43 s')
    expect(text).toContain('alice@example.com')
    expect(text).toContain('claude-sonnet-4-5')
    expect(text).toContain('bob@example.com')
    expect(text).toContain('admin.ops.realtimeRequests.failureReasons.upstream_timeout')
    expect(text).toContain('admin.ops.realtimeRequests.phases.bodyRead')
    expect(text).toContain('admin.ops.realtimeRequests.phases.finish')

    wrapper.unmount()
  })

  it('triggers API queries when changing result, model, limit, and debounced search filters', async () => {
    const wrapper = mountCard()
    await flushPromises()
    mockGetRealtimeRequests.mockClear()

    // Click "failure" segmented button
    const failureBtn = wrapper
      .findAll('button')
      .find((b) => b.text() === 'admin.ops.realtimeRequests.filters.failure')
    expect(failureBtn).toBeDefined()
    await failureBtn!.trigger('click')
    await flushPromises()
    expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
      { limit: 100, result: 'failure' },
      expect.any(Object)
    )

    // Change model selector
    const selects = wrapper.findAll('select.select-stub')
    expect(selects.length).toBe(2)
    await selects[0]!.setValue('gpt-5')
    await flushPromises()
    expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
      { limit: 100, result: 'failure', model: 'gpt-5' },
      expect.any(Object)
    )

    // Change limit selector
    await selects[1]!.setValue('200')
    await flushPromises()
    expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
      { limit: 200, result: 'failure', model: 'gpt-5' },
      expect.any(Object)
    )

    // Type search query (debounced 300ms)
    mockGetRealtimeRequests.mockClear()
    const searchInput = wrapper.find('input[type="text"]')
    await searchInput.setValue('req-beta')
    vi.advanceTimersByTime(250)
    await flushPromises()
    expect(mockGetRealtimeRequests).not.toHaveBeenCalled()

    vi.advanceTimersByTime(100)
    await flushPromises()
    expect(mockGetRealtimeRequests).toHaveBeenCalledWith(
      { limit: 200, result: 'failure', model: 'gpt-5', search: 'req-beta' },
      expect.any(Object)
    )

    wrapper.unmount()
  })

  it('opens OpsRealtimeRequestDetailModal with full latency and byte flow details on row/button click', async () => {
    const wrapper = mountCard()
    await flushPromises()

    const detailButtons = wrapper
      .findAll('button')
      .filter((b) => b.text() === 'admin.ops.realtimeRequests.table.detail')
    expect(detailButtons.length).toBe(2)

    await detailButtons[0]!.trigger('click')
    await flushPromises()

    const modalComp = wrapper.findComponent(OpsRealtimeRequestDetailModal)
    expect(modalComp.props('show')).toBe(true)
    expect(modalComp.props('item')?.request_id).toBe('req-alpha-001')

    const modalEl = wrapper.find('[data-testid="detail-modal"]')
    expect(modalEl.exists()).toBe(true)
    const modalText = modalEl.text()
    expect(modalText).toContain('req-alpha-001')
    expect(modalText).toContain('cli-001')
    expect(modalText).toContain('gw-node-1')
    expect(modalText).toContain('1.20 s')
    expect(modalText).toContain('610 ms')
    expect(modalText).toContain('2.0 KB')
    expect(modalText).toContain('admin.ops.realtimeRequests.byteStatus.complete')
    expect(modalText).toContain('admin.ops.realtimeRequests.byteStatus.partial')
    expect(modalText).toContain('admin.ops.realtimeRequests.byteStatus.unknown')

    wrapper.unmount()
  })

  it('renders disabled hint banner and pauses auto-refresh when enabled is false', async () => {
    mockGetRealtimeRequests.mockResolvedValue({
      enabled: false,
      buffer_size: 0,
      ttl_hours: 0,
      total_in_buffer: 0,
      summary: {
        total: 0,
        success: 0,
        failure: 0,
        success_rate: 0,
        avg_total_ms: 0,
        slow_count: 0
      },
      items: [],
      models: [],
      timestamp: '2026-11-21T06:10:06Z'
    })

    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.text()).toContain('admin.ops.realtimeRequests.disabledTitle')
    expect(wrapper.text()).toContain('admin.ops.realtimeRequests.disabledHint')

    // Ensure 5s auto-refresh does not keep firing when disabled
    mockGetRealtimeRequests.mockClear()
    vi.advanceTimersByTime(10000)
    await flushPromises()
    expect(mockGetRealtimeRequests).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('defines matching realtimeRequests translations in both zh and en locales', () => {
    expect(zhLocale.admin.ops.realtimeRequests.title).toBe('实时请求流')
    expect(enLocale.admin.ops.realtimeRequests.title).toBe('Realtime Request Stream')
    expect(zhLocale.admin.ops.realtimeRequests.disabledHint).toContain('MONITORING_OPS_SAMPLES_ENABLED=true')
    expect(enLocale.admin.ops.realtimeRequests.disabledHint).toContain('MONITORING_OPS_SAMPLES_ENABLED=true')
  })

  it('classifies all backend structured non-success results as failures even on HTTP 200 and maps all canonical failure reasons', () => {
    for (const result of ['upstream_error', 'timeout', 'client_abort', 'gateway_error', 'rejected']) {
      expect(
        isRequestSuccess({
          observed_at: '2026-11-21T06:10:00Z',
          request_id: `req-${result}`,
          result,
          http_status: 200
        })
      ).toBe(false)
    }
    expect(
      isRequestSuccess({
        observed_at: '2026-11-21T06:10:00Z',
        request_id: 'req-ok',
        result: 'success',
        http_status: 200
      })
    ).toBe(true)

    const canonicalReasons = [
      'upstream_rate_limit',
      'upstream_auth',
      'upstream_model_or_request',
      'upstream_server_error',
      'upstream_connect_timeout',
      'upstream_read_timeout',
      'proxy_unreachable',
      'client_abort_during_body_read',
      'client_abort_during_response',
      'gateway_panic',
      'gateway_db',
      'gateway_ratelimit',
      'unknown'
    ] as const
    const identityT = (key: string) => key
    for (const reason of canonicalReasons) {
      expect(getFailureReasonLabel(reason, identityT)).toBe(
        `admin.ops.realtimeRequests.failureReasons.${reason}`
      )
      expect((zhLocale.admin.ops.realtimeRequests.failureReasons as Record<string, string>)[reason]).toBeTruthy()
      expect((enLocale.admin.ops.realtimeRequests.failureReasons as Record<string, string>)[reason]).toBeTruthy()
    }
  })
})
