import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const { getTiming } = vi.hoisted(() => ({ getTiming: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { getTiming } }))

import RequestTimingDrawer from '../RequestTimingDrawer.vue'
import type { RequestTiming, RequestTimingDetail } from '@/types/requestTiming'

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

const detail: RequestTimingDetail = {
  id: 4242,
  created_at: '2026-05-01T10:00:00Z',
  account_id: 7,
  group_id: 3,
  model: 'claude-sonnet-4',
  request_id: 'req_abc',
  client_request_id: 'client_xyz',
  gateway_request_id: 'gw_123',
  upstream_request_id: 'up_456',
  stream: true,
  request_type: 2,
  timing,
}

const mountDrawer = (props: Record<string, unknown>) =>
  mount(RequestTimingDrawer, {
    props: { show: true, ...props },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        HelpTooltip: true,
        Icon: true,
      },
    },
  })

/** 读取 meta 网格里某一行的值（label 在此 spec 里是原始 i18n key）。 */
const metaValue = (wrapper: ReturnType<typeof mountDrawer>, labelKey: string): string => {
  const label = wrapper.findAll('dt').find((node) => node.text() === labelKey)
  const dd = label?.element.parentElement?.querySelector('dd')
  return (dd?.textContent || '').trim()
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('RequestTimingDrawer', () => {
  it('uses the embedded timing for ops rows and never calls the usage detail API', async () => {
    const wrapper = mountDrawer({ log: detail, source: 'ops' })
    await flushPromises()

    expect(getTiming).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('client_xyz')
    expect(wrapper.text()).toContain('gw_123')
    expect(wrapper.text()).toContain('up_456')
    expect(wrapper.find('[data-testid="request-timing-drawer"]').exists()).toBe(true)
  })

  it('derives the client id from a client:-prefixed request id on usage rows', async () => {
    const wrapper = mountDrawer({
      log: {
        id: 4300,
        created_at: '2026-05-01T10:00:00Z',
        model: 'gpt-4.1',
        stream: true,
        account_id: 3,
        request_id: 'client:b7f1c2d3-4e5f-6789-abcd-ef0123456789',
        timing,
      },
      source: 'usage',
    })
    await flushPromises()

    // 列表 DTO 已内嵌 timing → 不再请求详情，但客户端 id 仍必须可 grep
    expect(getTiming).not.toHaveBeenCalled()
    expect(metaValue(wrapper, 'usage.timing.meta.clientRequestId')).toBe('b7f1c2d3-4e5f-6789-abcd-ef0123456789')
  })

  it('keeps an explicit client id and never guesses gateway or upstream ids', async () => {
    const wrapper = mountDrawer({
      log: { ...detail, request_id: 'client:derived-should-lose', client_request_id: 'client_explicit' },
      source: 'ops',
    })
    await flushPromises()

    expect(metaValue(wrapper, 'usage.timing.meta.clientRequestId')).toBe('client_explicit')
    // request_id 不带 `client:` 前缀时不派生，也不拿 request_id 冒充其它 id
    const plain = mountDrawer({ log: { ...detail, request_id: 'req_plain', client_request_id: null }, source: 'ops' })
    await flushPromises()
    expect(metaValue(plain, 'usage.timing.meta.clientRequestId')).toBe('—')
    expect(metaValue(plain, 'usage.timing.meta.requestId')).toBe('req_plain')
  })

  it('fetches the detail once for a usage row without embedded timing', async () => {
    getTiming.mockResolvedValue(detail)
    const wrapper = mountDrawer({
      log: {
        id: 4242,
        created_at: '2026-05-01T10:00:00Z',
        model: 'claude-sonnet-4',
        stream: true,
        account_id: 7,
        request_id: 'req_abc',
      },
      source: 'usage',
    })
    await flushPromises()

    expect(getTiming).toHaveBeenCalledTimes(1)
    expect(getTiming).toHaveBeenCalledWith(4242, '2026-05-01T10:00:00Z', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.text()).toContain('client_xyz')
  })

  it('drops a stale response when the user switches rows quickly', async () => {
    let resolveFirst: (value: RequestTimingDetail) => void = () => {}
    let resolveSecond: (value: RequestTimingDetail) => void = () => {}
    getTiming
      .mockReturnValueOnce(new Promise<RequestTimingDetail>((resolve) => { resolveFirst = resolve }))
      .mockReturnValueOnce(new Promise<RequestTimingDetail>((resolve) => { resolveSecond = resolve }))

    const wrapper = mountDrawer({
      log: { id: 1, created_at: '2026-05-01T10:00:00Z', model: 'm1', stream: true, account_id: 1, request_id: 'r1' },
      source: 'usage',
    })
    await flushPromises()

    await wrapper.setProps({
      log: { id: 2, created_at: '2026-05-01T10:00:05Z', model: 'm2', stream: true, account_id: 1, request_id: 'r2' },
    })
    await flushPromises()
    expect(getTiming).toHaveBeenCalledTimes(2)

    resolveSecond({ ...detail, id: 2, client_request_id: 'client_second' })
    await flushPromises()
    resolveFirst({ ...detail, id: 1, client_request_id: 'client_first' })
    await flushPromises()

    expect(wrapper.text()).toContain('client_second')
    expect(wrapper.text()).not.toContain('client_first')
  })
})
