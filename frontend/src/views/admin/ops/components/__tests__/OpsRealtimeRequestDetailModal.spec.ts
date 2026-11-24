import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsRealtimeRequestDetailModal from '../OpsRealtimeRequestDetailModal.vue'
import type { OpsRealtimeRequestItem } from '@/api/admin/ops'

const mockCopyToClipboard = vi.fn()
const mockShowWarning = vi.fn()

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

const baseFailedItem: OpsRealtimeRequestItem = {
  observed_at: '2026-11-24T01:30:00.123Z',
  request_id: 'req-fail-001',
  client_request_id: 'cli-fail-001',
  user_id: 10,
  user_email: 'alice@example.com',
  api_key_id: 20,
  api_key_name: 'Prod Key',
  api_key_masked: 'sk-***1234',
  account_id: 30,
  account_name: 'openai-prod-1',
  group_id: 2,
  group_name: 'primary-group',
  platform: 'openai',
  model: 'gpt-5',
  endpoint_class: 'responses',
  endpoint: '/v1/responses',
  http_method: 'post',
  result: 'upstream_error',
  http_status: 502,
  failure_reason: 'upstream_rate_limit',
  error_message: 'Too many requests from upstream\nRetry after 10s',
  upstream_status: 429,
  upstream_error_code: 'rate_limit_exceeded',
  attempt_count: 3,
  failed_attempts: 3,
  node: 'gw-node-1',
  total_ms: 1500
}

describe('OpsRealtimeRequestDetailModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockCopyToClipboard.mockResolvedValue(true)
  })

  const mountModal = (item: OpsRealtimeRequestItem | null, show = true) =>
    mount(OpsRealtimeRequestDetailModal, {
      props: { show, item },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          Icon: IconStub
        }
      }
    })

  it('renders red failure diagnostic banner directly below the top 4 KPI grid on failure', async () => {
    const wrapper = mountModal(baseFailedItem)
    await flushPromises()

    const kpiGrid = wrapper.get('[data-testid="detail-kpi-grid"]')
    const banner = wrapper.get('[data-testid="failure-diagnostic-banner"]')

    expect(kpiGrid.element.nextElementSibling).toBe(banner.element)
    expect(banner.classes()).toContain('border-red-200')

    const bannerText = banner.text()
    expect(bannerText).toContain('admin.ops.realtimeRequests.detail.failureBanner.title')
    expect(bannerText).toContain('admin.ops.realtimeRequests.failureReasons.upstream_rate_limit')
    expect(banner.get('[data-testid="failure-raw-reason"]').text()).toBe('upstream_rate_limit')
    expect(banner.get('[data-testid="failure-upstream-status"]').text()).toContain('429')
    expect(banner.get('[data-testid="failure-upstream-error-code"]').text()).toContain(
      'admin.ops.realtimeRequests.detail.failureBanner.upstreamErrorCode'
    )
    expect(banner.get('[data-testid="failure-upstream-error-code"]').text()).toContain('rate_limit_exceeded')
    expect(banner.get('[data-testid="failure-retry-count"]').text()).toContain('2')
    expect(banner.get('[data-testid="failure-attempt-count"]').text()).toContain('3')
    expect(banner.get('[data-testid="failure-failed-attempts"]').text()).toContain('3')
    expect(banner.get('[data-testid="failure-error-message"]').text()).toBe(
      'Too many requests from upstream\nRetry after 10s'
    )

    wrapper.unmount()
  })

  it('renders failure banner even when HTTP status is 200 if result is non-success', async () => {
    const wrapper = mountModal({
      ...baseFailedItem,
      http_status: 200,
      result: 'upstream_error'
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="failure-diagnostic-banner"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('copies trimmed error_message on click and warns if copy fails', async () => {
    const wrapper = mountModal({
      ...baseFailedItem,
      error_message: '  Too many requests from upstream  '
    })
    await flushPromises()

    const copyBtn = wrapper.get('[data-testid="copy-error-message-btn"]')
    await copyBtn.trigger('click')
    await flushPromises()

    expect(mockCopyToClipboard).toHaveBeenCalledWith(
      'Too many requests from upstream',
      'admin.ops.realtimeRequests.detail.copied'
    )
    expect(mockShowWarning).not.toHaveBeenCalled()

    mockCopyToClipboard.mockResolvedValueOnce(false)
    await copyBtn.trigger('click')
    await flushPromises()
    expect(mockShowWarning).toHaveBeenCalledWith('admin.ops.realtimeRequests.detail.copyFailed')

    wrapper.unmount()
  })

  it('gracefully falls back when error_message, upstream_status, and upstream_error_code are absent', async () => {
    const wrapper = mountModal({
      ...baseFailedItem,
      http_method: null,
      error_message: '   ',
      upstream_status: null,
      upstream_error_code: null,
      attempt_count: null,
      failed_attempts: null
    })
    await flushPromises()

    const banner = wrapper.get('[data-testid="failure-diagnostic-banner"]')
    expect(banner.find('[data-testid="failure-upstream-status"]').exists()).toBe(false)
    expect(banner.find('[data-testid="failure-upstream-error-code"]').exists()).toBe(false)
    expect(banner.find('[data-testid="copy-error-message-btn"]').exists()).toBe(false)
    expect(banner.find('[data-testid="failure-error-message"]').exists()).toBe(false)
    expect(banner.get('[data-testid="failure-error-message-empty"]').text()).toBe(
      'admin.ops.realtimeRequests.detail.failureBanner.emptyErrorMessage'
    )
    expect(banner.get('[data-testid="failure-retry-count"]').text()).toContain('0')
    expect(banner.get('[data-testid="failure-attempt-count"]').text()).toContain('—')
    expect(banner.get('[data-testid="failure-failed-attempts"]').text()).toContain('—')
    expect(wrapper.find('[data-testid="detail-http-method"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('uses generic errorCode label when rejection occurs locally without upstream status or attempts', async () => {
    const wrapper = mountModal({
      ...baseFailedItem,
      http_status: 402,
      result: 'rejected',
      failure_reason: 'gateway_ratelimit',
      upstream_status: null,
      upstream_error_code: 'INSUFFICIENT_BALANCE',
      attempt_count: 0,
      failed_attempts: 0,
      error_message: 'Insufficient account balance'
    })
    await flushPromises()

    const codeChip = wrapper.get('[data-testid="failure-upstream-error-code"]')
    expect(codeChip.text()).toContain('admin.ops.realtimeRequests.detail.failureBanner.errorCode')
    expect(codeChip.text()).not.toContain('upstreamErrorCode')
    expect(codeChip.text()).toContain('INSUFFICIENT_BALANCE')

    wrapper.unmount()
  })

  it('hides failure banner on success (200 and 101) and displays normalized http_method in Basic Info', async () => {
    const wrapper = mountModal({
      ...baseFailedItem,
      result: 'success',
      http_status: 101,
      failure_reason: null,
      error_message: null,
      upstream_status: null,
      upstream_error_code: null,
      http_method: 'get',
      endpoint: '/v1/realtime',
      endpoint_class: 'realtime'
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="failure-diagnostic-banner"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="detail-http-method"]').text()).toBe('GET')
    expect(wrapper.text()).toContain('/v1/realtime')
    expect(wrapper.text()).toContain('realtime')

    wrapper.unmount()
  })
})
