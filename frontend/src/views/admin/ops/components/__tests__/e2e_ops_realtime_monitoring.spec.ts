import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import OpsRealtimeRequestsCard from '../OpsRealtimeRequestsCard.vue'
import {
  CANONICAL_FAILURE_REASON_KEYS,
  computePhaseBreakdown,
  formatByteSize,
  formatDurationMs,
  formatEndpointWithMethod,
  getFailureReasonLabel,
  getFailureReasonShortLabel,
  isRequestSuccess
} from '../../utils/realtimeRequestFormatters'
import {
  opsAPI,
  type OpsRealtimeRequestItem,
  type OpsRealtimeRequestsParams,
  type OpsRealtimeRequestsResponse
} from '@/api/admin/ops'
import { i18n as singletonI18n } from '@/i18n'
import enLocale from '@/i18n/locales/en'
import zhLocale from '@/i18n/locales/zh'
import { useAppStore } from '@/stores'

const mockGetRealtimeRequests = vi.fn()

vi.mock('@/api/admin/ops', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/ops')>()
  const mockedOpsAPI = {
    ...actual.opsAPI,
    getRealtimeRequests: (...args: unknown[]) => mockGetRealtimeRequests(...args)
  }
  return {
    ...actual,
    opsAPI: mockedOpsAPI,
    default: mockedOpsAPI
  }
})

type LocaleCode = 'zh' | 'en'

const CJK_REGEX = /[\u3400-\u9fff]/u
const RAW_I18N_KEY_REGEX = /admin\.ops\.realtimeRequests\.[a-zA-Z0-9_.]+/

const EXISTING_CANONICAL_REASONS = [
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

const NEW_4XX_AND_RESULT_REASONS = [
  'client_bad_request',
  'client_auth_failed',
  'client_forbidden',
  'client_not_found',
  'body_too_large',
  'client_abort'
] as const

const LEGACY_RESULT_FALLBACK_REASONS = [
  'gateway_error',
  'rejected'
] as const

const ALL_CONTRACT_REASONS = [
  ...EXISTING_CANONICAL_REASONS,
  ...NEW_4XX_AND_RESULT_REASONS
] as const

const R2_4XX_MATRIX: Array<{
  httpStatus: number
  result: string
  failureReason: string
  method: string
  endpoint: string
  errorMessage: string
}> = [
  {
    httpStatus: 400,
    result: 'rejected',
    failureReason: 'client_bad_request',
    method: 'POST',
    endpoint: '/v1/responses',
    errorMessage: 'Invalid JSON payload: missing required field "model"'
  },
  {
    httpStatus: 401,
    result: 'rejected',
    failureReason: 'client_auth_failed',
    method: 'POST',
    endpoint: '/v1/messages',
    errorMessage: 'API key authentication failed: key revoked'
  },
  {
    httpStatus: 403,
    result: 'rejected',
    failureReason: 'client_forbidden',
    method: 'POST',
    endpoint: '/v1/chat/completions',
    errorMessage: 'Model claude-opus-4-5 is not permitted for group default'
  },
  {
    httpStatus: 404,
    result: 'rejected',
    failureReason: 'client_not_found',
    method: 'GET',
    endpoint: '/v1/models/unknown-model',
    errorMessage: 'Requested route or model resource not found'
  },
  {
    httpStatus: 413,
    result: 'rejected',
    failureReason: 'body_too_large',
    method: 'POST',
    endpoint: '/v1/responses',
    errorMessage: 'Request body exceeds 10485760 byte limit'
  },
  {
    httpStatus: 429,
    result: 'rejected',
    failureReason: 'gateway_ratelimit',
    method: 'POST',
    endpoint: '/v1/messages',
    errorMessage: 'Concurrency limit exceeded for API key'
  },
  {
    httpStatus: 499,
    result: 'client_abort',
    failureReason: 'client_abort',
    method: 'POST',
    endpoint: '/v1/responses',
    errorMessage: 'Client closed connection before upstream response completed'
  }
]

function makeRealtimeItem(
  overrides: Partial<OpsRealtimeRequestItem> & Record<string, unknown> = {}
): OpsRealtimeRequestItem {
  const base: Record<string, unknown> = {
    observed_at: '2026-11-24T01:20:00.123Z',
    request_id: 'req-e2e-default-001',
    client_request_id: 'cli-e2e-001',
    user_id: 101,
    user_email: 'operator@example.com',
    api_key_id: 202,
    api_key_name: 'Ops Primary Key',
    api_key_masked: 'sk-***abcd',
    account_id: 303,
    account_name: 'upstream-pool-1',
    group_id: 7,
    group_name: 'prod-group',
    platform: 'openai',
    model: 'gpt-5',
    endpoint_class: 'responses',
    endpoint: '/v1/responses',
    http_method: 'POST',
    result: 'success',
    http_status: 200,
    failure_reason: null,
    error_message: null,
    upstream_status: null,
    upstream_error_code: null,
    attempt_count: 1,
    failed_attempts: 0,
    node: 'gw-hz-01',
    body_read_ms: 8,
    wait_ms: 4,
    gateway_ms: 12,
    upstream_ms: 420,
    headers_end_ms: 310,
    response_ms: 180,
    finish_ms: 6,
    total_ms: 630,
    bytes_in_declared: 1024,
    bytes_in_wire: 1024,
    bytes_in_decoded: 1024,
    bytes_up_req_total: 1280,
    bytes_resp_upstream: 4096,
    bytes_resp_downstream: 4096,
    bytes_in_wire_status: 'complete',
    bytes_up_req_total_status: 'complete',
    bytes_resp_upstream_status: 'complete',
    bytes_resp_downstream_status: 'complete'
  }
  return { ...base, ...overrides } as OpsRealtimeRequestItem
}

function makeRealtimeResponse(
  items: OpsRealtimeRequestItem[],
  overrides: Partial<OpsRealtimeRequestsResponse> & Record<string, unknown> = {}
): OpsRealtimeRequestsResponse {
  const successCount = items.filter((it) => isRequestSuccess(it)).length
  const failureCount = items.length - successCount
  const totalMsSum = items.reduce((acc, it) => acc + (typeof it.total_ms === 'number' ? it.total_ms : 0), 0)
  const slowCount = items.filter((it) => (it.total_ms ?? 0) >= 3000).length
  const models = Array.from(new Set(items.map((it) => it.model).filter((m): m is string => Boolean(m))))
  const failureReasons = Array.from(
    new Set(
      items
        .filter((it) => !isRequestSuccess(it))
        .map((it) => it.failure_reason)
        .filter((r): r is string => Boolean(r && r.trim()))
    )
  ).sort()

  const base: Record<string, unknown> = {
    enabled: true,
    buffer_size: 2000,
    ttl_hours: 24,
    total_in_buffer: items.length,
    summary: {
      total: items.length,
      success: successCount,
      failure: failureCount,
      success_rate: items.length > 0 ? (successCount / items.length) * 100 : 0,
      avg_total_ms: items.length > 0 ? totalMsSum / items.length : 0,
      slow_count: slowCount
    },
    items,
    models,
    failure_reasons: failureReasons,
    timestamp: '2026-11-24T01:25:00Z'
  }
  return { ...base, ...overrides } as OpsRealtimeRequestsResponse
}

function getNestedValue(root: Record<string, unknown>, path: string): unknown {
  let cursor: unknown = root
  for (const segment of path.split('.')) {
    if (!cursor || typeof cursor !== 'object' || !(segment in (cursor as Record<string, unknown>))) {
      return undefined
    }
    cursor = (cursor as Record<string, unknown>)[segment]
  }
  return cursor
}

function flattenLeafStrings(obj: Record<string, unknown>, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      Object.assign(out, flattenLeafStrings(value as Record<string, unknown>, fullKey))
    } else {
      out[fullKey] = typeof value === 'string' ? value : String(value ?? '')
    }
  }
  return out
}

function createTestI18n(locale: LocaleCode = 'zh') {
  return createI18n({
    legacy: false,
    locale,
    fallbackLocale: 'en',
    messages: {
      en: enLocale,
      zh: zhLocale
    }
  })
}

const activeWrappers: VueWrapper[] = []

let originalMatchMedia: typeof window.matchMedia
let originalIsSecureContextDescriptor: PropertyDescriptor | undefined
let originalClipboardDescriptor: PropertyDescriptor | undefined
let originalExecCommandDescriptor: PropertyDescriptor | undefined
let clipboardWriteMock: ReturnType<typeof vi.fn>

function setSecureContextAndClipboard(secure: boolean, writeImpl: (text: string) => Promise<void>) {
  Object.defineProperty(window, 'isSecureContext', {
    configurable: true,
    value: secure
  })
  clipboardWriteMock = vi.fn(writeImpl)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: clipboardWriteMock }
  })
}

function setViewportMatches(desktop: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: desktop,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn()
  }))
}

function mountRealtimeCard(locale: LocaleCode = 'zh') {
  const pinia = createPinia()
  setActivePinia(pinia)
  const i18n = createTestI18n(locale)
  singletonI18n.global.locale.value = locale

  const wrapper = mount(OpsRealtimeRequestsCard, {
    global: {
      plugins: [pinia, i18n],
      stubs: {
        teleport: true
      }
    }
  })
  activeWrappers.push(wrapper)
  return { wrapper, i18n, appStore: useAppStore() }
}

function findSegmentedButton(wrapper: VueWrapper, label: string) {
  const group = wrapper.find('[role="group"]')
  expect(group.exists()).toBe(true)
  const btn = group.findAll('button').find((b) => b.text().trim() === label)
  expect(btn, `Segmented button "${label}" should exist`).toBeDefined()
  return btn!
}

function findSelectTriggers(wrapper: VueWrapper) {
  return wrapper.findAll('button[aria-haspopup="true"]')
}

async function openSelectAndChooseOption(
  wrapper: VueWrapper,
  triggerMatcher: (triggerText: string) => boolean,
  optionLabel: string
) {
  const triggers = findSelectTriggers(wrapper)
  const trigger = triggers.find((t) => triggerMatcher(t.text().trim()))
  expect(trigger, `Select trigger matching predicate should exist among [${triggers.map((t) => t.text().trim()).join(', ')}]`).toBeDefined()

  await trigger!.trigger('click')
  await flushPromises()

  const listboxes = wrapper.findAll('[role="listbox"]')
  expect(listboxes.length, 'Real Select listbox should render inside wrapper').toBeGreaterThan(0)
  const activeListbox = listboxes[listboxes.length - 1]!
  const options = activeListbox.findAll('[role="option"]')
  const targetOption = options.find((opt) => opt.text().trim() === optionLabel)
  expect(
    targetOption,
    `Option "${optionLabel}" should exist in listbox [${options.map((o) => o.text().trim()).join(', ')}]`
  ).toBeDefined()

  await targetOption!.trigger('click')
  await flushPromises()
}

function findDialog(wrapper: VueWrapper) {
  const dialog = wrapper.find('[role="dialog"]')
  expect(dialog.exists(), 'BaseDialog [role="dialog"] should be open').toBe(true)
  return dialog
}

function findFailureBannerSection(dialog: ReturnType<VueWrapper['find']>, locale: LocaleCode) {
  const titleText = locale === 'zh' ? '失败原因与错误详情' : 'Failure Reason & Error Details'
  const sections = dialog.findAll('section')
  return sections.find((sec) => sec.text().includes(titleText))
}

function findBasicInfoSection(dialog: ReturnType<VueWrapper['find']>, locale: LocaleCode) {
  const titleText = locale === 'zh' ? '基本信息' : 'Basic Info'
  const sections = dialog.findAll('section')
  return sections.find((sec) => sec.text().includes(titleText))
}

function findKpiGridElement(dialog: ReturnType<VueWrapper['find']>, locale: LocaleCode) {
  const totalLabel = locale === 'zh' ? '总耗时' : 'Total Latency'
  const statusLabel = locale === 'zh' ? 'HTTP 状态' : 'HTTP Status'
  const candidates = dialog.findAll('div').filter((el) => {
    const txt = el.text()
    return txt.includes(totalLabel) && txt.includes(statusLabel)
  })
  return candidates[candidates.length - 1]
}

describe('E2E Opaque-Box: Frontend Ops Realtime Monitoring & Troubleshooting (F10-F12)', () => {
  beforeAll(() => {
    singletonI18n.global.setLocaleMessage('en', enLocale)
    singletonI18n.global.setLocaleMessage('zh', zhLocale)
  })

  beforeEach(() => {
    vi.clearAllMocks()
    originalMatchMedia = window.matchMedia
    originalIsSecureContextDescriptor = Object.getOwnPropertyDescriptor(window, 'isSecureContext')
    originalClipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    originalExecCommandDescriptor = Object.getOwnPropertyDescriptor(document, 'execCommand')

    setViewportMatches(true)
    setSecureContextAndClipboard(true, async () => undefined)
  })

  afterEach(() => {
    while (activeWrappers.length > 0) {
      activeWrappers.pop()?.unmount()
    }
    vi.useRealTimers()
    window.matchMedia = originalMatchMedia

    if (originalIsSecureContextDescriptor) {
      Object.defineProperty(window, 'isSecureContext', originalIsSecureContextDescriptor)
    } else {
      delete (window as unknown as Record<string, unknown>).isSecureContext
    }
    if (originalClipboardDescriptor) {
      Object.defineProperty(navigator, 'clipboard', originalClipboardDescriptor)
    } else {
      delete (navigator as unknown as Record<string, unknown>).clipboard
    }
    if (originalExecCommandDescriptor) {
      Object.defineProperty(document, 'execCommand', originalExecCommandDescriptor)
    }
  })

  // =========================================================================
  // Tier 1: Primary Feature Coverage (>= 5 distinct cases per feature F10-F12)
  // =========================================================================

  describe('Tier 1 — F10: Frontend API Types, Formatters & Bilingual i18n Contracts', () => {
    it('T1-F10-1: translates all 13 existing canonical failure_reason enums in zh (with CJK) and en (zero CJK)', () => {
      const zhI18n = createTestI18n('zh')
      const enI18n = createTestI18n('en')

      for (const reason of EXISTING_CANONICAL_REASONS) {
        const zhLabel = getFailureReasonLabel(reason, zhI18n.global.t)
        const enLabel = getFailureReasonLabel(reason, enI18n.global.t)
        const zhShort = getFailureReasonShortLabel(reason, zhI18n.global.t)
        const enShort = getFailureReasonShortLabel(reason, enI18n.global.t)

        expect(zhLabel).not.toBe(reason)
        expect(zhLabel).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(zhLabel).toMatch(CJK_REGEX)

        expect(enLabel).not.toBe(reason)
        expect(enLabel).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(enLabel).not.toMatch(CJK_REGEX)

        expect(zhShort.trim().length).toBeGreaterThan(0)
        expect(zhShort).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(zhShort).toMatch(CJK_REGEX)

        expect(enShort.trim().length).toBeGreaterThan(0)
        expect(enShort).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(enShort).not.toMatch(CJK_REGEX)
      }
    })

    it('T1-F10-2: translates all new 4xx/499 & structured result failure_reason enums in zh and en', () => {
      const zhI18n = createTestI18n('zh')
      const enI18n = createTestI18n('en')

      expect([...ALL_CONTRACT_REASONS].sort()).toEqual([...CANONICAL_FAILURE_REASON_KEYS].sort())

      for (const reason of NEW_4XX_AND_RESULT_REASONS) {
        expect(CANONICAL_FAILURE_REASON_KEYS).toContain(reason)

        const zhLabel = getFailureReasonLabel(reason, zhI18n.global.t)
        const enLabel = getFailureReasonLabel(reason, enI18n.global.t)
        const zhShort = getFailureReasonShortLabel(reason, zhI18n.global.t)
        const enShort = getFailureReasonShortLabel(reason, enI18n.global.t)

        expect(zhLabel).not.toBe(reason)
        expect(zhLabel).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(zhLabel).toMatch(CJK_REGEX)

        expect(enLabel).not.toBe(reason)
        expect(enLabel).not.toMatch(RAW_I18N_KEY_REGEX)
        expect(enLabel).not.toMatch(CJK_REGEX)

        expect(zhShort).not.toBe(reason)
        expect(zhShort).toMatch(CJK_REGEX)
        expect(enShort).not.toBe(reason)
        expect(enShort).not.toMatch(CJK_REGEX)
      }

      for (const legacyReason of LEGACY_RESULT_FALLBACK_REASONS) {
        expect(CANONICAL_FAILURE_REASON_KEYS).not.toContain(legacyReason)
        expect(getFailureReasonLabel(legacyReason, zhI18n.global.t)).toMatch(CJK_REGEX)
        expect(getFailureReasonLabel(legacyReason, enI18n.global.t)).not.toMatch(CJK_REGEX)
        expect(getFailureReasonShortLabel(legacyReason, zhI18n.global.t)).toMatch(CJK_REGEX)
        expect(getFailureReasonShortLabel(legacyReason, enI18n.global.t)).not.toMatch(CJK_REGEX)
      }
    })

    it('T1-F10-3: enforces strict zh/en leaf key symmetry and zero CJK leakage in en.admin.ops.realtimeRequests', () => {
      const zhRt = getNestedValue(zhLocale as unknown as Record<string, unknown>, 'admin.ops.realtimeRequests') as Record<string, unknown>
      const enRt = getNestedValue(enLocale as unknown as Record<string, unknown>, 'admin.ops.realtimeRequests') as Record<string, unknown>

      expect(zhRt).toBeDefined()
      expect(enRt).toBeDefined()

      const zhLeaves = flattenLeafStrings(zhRt)
      const enLeaves = flattenLeafStrings(enRt)

      expect(Object.keys(zhLeaves).sort()).toEqual(Object.keys(enLeaves).sort())
      for (const [key, enVal] of Object.entries(enLeaves)) {
        expect(enVal.trim().length, `en.${key} must be non-empty`).toBeGreaterThan(0)
        expect(enVal, `en.${key} must not contain CJK`).not.toMatch(CJK_REGEX)
        expect(zhLeaves[key]!.trim().length, `zh.${key} must be non-empty`).toBeGreaterThan(0)
      }
    })

    it('T1-F10-4: classifies all 6 backend result enum values plus HTTP 101 Switching Protocols accurately', () => {
      expect(
        isRequestSuccess(
          makeRealtimeItem({
            result: 'success',
            http_status: 200
          })
        )
      ).toBe(true)

      expect(
        isRequestSuccess(
          makeRealtimeItem({
            result: 'success',
            http_status: 101
          })
        )
      ).toBe(true)

      for (const failedResult of ['upstream_error', 'timeout', 'client_abort', 'gateway_error', 'rejected']) {
        expect(
          isRequestSuccess(
            makeRealtimeItem({
              result: failedResult,
              http_status: 200
            })
          ),
          `result="${failedResult}" must be treated as failure even with http_status=200`
        ).toBe(false)
      }
    })

    it('T1-F10-5: formats http_method + endpoint consistently and preserves OpsRealtimeRequestItem/Params/Response contract', async () => {
      expect(formatEndpointWithMethod('post', '/v1/responses', 'responses')).toBe('POST /v1/responses')
      expect(formatEndpointWithMethod('GET', '/v1/models', 'models')).toBe('GET /v1/models')

      const params: OpsRealtimeRequestsParams & Record<string, unknown> = {
        limit: 50,
        result: 'failure',
        failure_reason: 'client_auth_failed',
        model: 'gpt-5',
        search: 'req-401'
      }
      expect(params.failure_reason).toBe('client_auth_failed')

      const item = makeRealtimeItem({
        http_method: 'POST',
        error_message: 'Upstream returned 429',
        upstream_status: 429,
        upstream_error_code: 'rate_limit_exceeded'
      })
      const resp = makeRealtimeResponse([item], { failure_reasons: ['upstream_rate_limit'] })
      mockGetRealtimeRequests.mockResolvedValueOnce(resp)

      const fetched = await opsAPI.getRealtimeRequests(params)
      expect((fetched as unknown as Record<string, unknown>).failure_reasons).toEqual(['upstream_rate_limit'])
      expect((fetched.items[0] as unknown as Record<string, unknown>).upstream_status).toBe(429)
      expect((fetched.items[0] as unknown as Record<string, unknown>).upstream_error_code).toBe('rate_limit_exceeded')
    })

    it('T1-F10-6: verifies detail failureBanner & filter i18n keys exist and interpolate dynamic values in zh and en', () => {
      const zhI18n = createTestI18n('zh')
      const enI18n = createTestI18n('en')

      const requiredNestedPaths = [
        'admin.ops.realtimeRequests.filters.allFailureReasons',
        'admin.ops.realtimeRequests.detail.failureBanner.title',
        'admin.ops.realtimeRequests.detail.failureBanner.upstreamStatus',
        'admin.ops.realtimeRequests.detail.failureBanner.upstreamErrorCode',
        'admin.ops.realtimeRequests.detail.failureBanner.retryCount',
        'admin.ops.realtimeRequests.detail.failureBanner.attemptCount',
        'admin.ops.realtimeRequests.detail.failureBanner.failedAttempts',
        'admin.ops.realtimeRequests.detail.failureBanner.errorMessage',
        'admin.ops.realtimeRequests.detail.failureBanner.emptyErrorMessage'
      ]

      for (const path of requiredNestedPaths) {
        const zhVal = getNestedValue(zhLocale as unknown as Record<string, unknown>, path)
        const enVal = getNestedValue(enLocale as unknown as Record<string, unknown>, path)
        expect(typeof zhVal, `Missing zh path ${path}`).toBe('string')
        expect(typeof enVal, `Missing en path ${path}`).toBe('string')
        expect(String(zhVal).trim().length).toBeGreaterThan(0)
        expect(String(enVal).trim().length).toBeGreaterThan(0)
      }

      expect(
        zhI18n.global.t('admin.ops.realtimeRequests.bufferStatus', { current: 12, capacity: 2000, ttl: 24 })
      ).toContain('12 / 2000')
      expect(
        enI18n.global.t('admin.ops.realtimeRequests.summary.totalSub', { success: 8, failure: 2 })
      ).toContain('OK 8 · Fail 2')
    })
  })

  describe('Tier 1 — F11: Detail Modal First-Screen Failure Diagnostic Banner', () => {
    it('T1-F11-1: opens modal and places red failure banner strictly below top 4 KPI tiles and above Basic Info Grid', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-order-check-001',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        upstream_status: 429,
        upstream_error_code: 'rate_limit_exceeded',
        error_message: 'Upstream quota exceeded on pool-1',
        attempt_count: 3,
        failed_attempts: 2
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const kpiGrid = findKpiGridElement(dialog, 'zh')
      const banner = findFailureBannerSection(dialog, 'zh')
      const basicInfo = findBasicInfoSection(dialog, 'zh')

      expect(kpiGrid).toBeDefined()
      expect(banner).toBeDefined()
      expect(basicInfo).toBeDefined()

      const kpiBeforeBanner =
        kpiGrid!.element.compareDocumentPosition(banner!.element) & Node.DOCUMENT_POSITION_FOLLOWING
      const bannerBeforeBasic =
        banner!.element.compareDocumentPosition(basicInfo!.element) & Node.DOCUMENT_POSITION_FOLLOWING

      expect(kpiBeforeBanner).toBeTruthy()
      expect(bannerBeforeBasic).toBeTruthy()
    })

    it('T1-F11-2: displays localized failure label, raw enum, upstream_status, and upstream_error_code in banner', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-upstream-meta-001',
        result: 'upstream_error',
        http_status: 502,
        failure_reason: 'upstream_rate_limit',
        upstream_status: 429,
        upstream_error_code: 'tokens_per_min_limit',
        error_message: 'Rate limit reached for gpt-5'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      const bannerText = banner.text()

      expect(bannerText).toContain('失败原因与错误详情')
      expect(bannerText).toContain('上游限流')
      expect(bannerText).toContain('upstream_rate_limit')
      expect(bannerText).toContain('429')
      expect(bannerText).toContain('tokens_per_min_limit')
    })

    it('T1-F11-3: displays retry count (attempt_count - 1), attempt_count, and failed_attempts in failure banner', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-retries-001',
        result: 'upstream_error',
        http_status: 500,
        failure_reason: 'upstream_server_error',
        attempt_count: 4,
        failed_attempts: 3,
        error_message: 'Upstream returned 500 internal error'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'en')!
      const bannerText = banner.text()

      expect(bannerText).toContain('Retries')
      expect(bannerText).toContain('3')
      expect(bannerText).toContain('Attempts')
      expect(bannerText).toContain('4')
      expect(bannerText).toContain('Failed Attempts')
    })

    it('T1-F11-4: renders full error_message and copies exact text to navigator.clipboard.writeText with success toast', async () => {
      const exactError = 'Upstream 429: Rate limit reached for org-redacted [sk-***]'
      const failItem = makeRealtimeItem({
        request_id: 'req-copy-err-001',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        error_message: exactError
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper, appStore } = mountRealtimeCard('zh')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      expect(banner.text()).toContain(exactError)

      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === '复制')
      expect(copyBtn).toBeDefined()
      await copyBtn!.trigger('click')
      await flushPromises()

      expect(clipboardWriteMock).toHaveBeenCalledTimes(1)
      expect(clipboardWriteMock).toHaveBeenCalledWith(exactError)
      expect(appStore.toasts.some((toast) => toast.type === 'success' && toast.message === '已复制')).toBe(true)
    })

    it('T1-F11-5: displays http_method badge alongside endpoint and supports copying request_id / client_request_id', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-method-copy-999',
        client_request_id: 'cli-trace-888',
        http_method: 'post',
        endpoint: '/v1/responses',
        endpoint_class: 'responses',
        result: 'rejected',
        http_status: 400,
        failure_reason: 'client_bad_request',
        error_message: 'Bad input schema'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const basicInfo = findBasicInfoSection(dialog, 'en')!
      const basicText = basicInfo.text()

      expect(basicText).toContain('POST')
      expect(basicText).toContain('/v1/responses')
      expect(basicText).toContain('responses')
      expect(basicText).toContain('req-method-copy-999')
      expect(basicText).toContain('cli-trace-888')

      const copyButtons = basicInfo.findAll('button').filter((b) => b.text().trim() === 'Copy')
      expect(copyButtons.length).toBeGreaterThanOrEqual(2)
      await copyButtons[0]!.trigger('click')
      await flushPromises()
      expect(clipboardWriteMock).toHaveBeenCalledWith('req-method-copy-999')
    })
  })

  describe('Tier 1 — F12: Realtime Requests Card Status, Method & Secondary Filter', () => {
    it('T1-F12-1: renders failure badge, short failure reason, HTTP status, and error_message hover title in Status column', async () => {
      const okItem = makeRealtimeItem({
        request_id: 'req-ok-01',
        result: 'success',
        http_status: 200
      })
      const failItem = makeRealtimeItem({
        request_id: 'req-fail-401',
        result: 'rejected',
        http_status: 401,
        failure_reason: 'client_auth_failed',
        error_message: 'Invalid bearer token sk-***xyz'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([okItem, failItem]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      const rows = wrapper.findAll('tbody tr')
      expect(rows).toHaveLength(2)

      expect(rows[0]!.text()).toContain('成功')
      expect(rows[0]!.find('[title="成功"]').exists()).toBe(true)

      const failRowText = rows[1]!.text()
      expect(failRowText).toContain('失败')
      expect(failRowText).toContain('鉴权失败')
      expect(failRowText).toContain('401')
      expect(rows[1]!.find('[title="Invalid bearer token sk-***xyz"]').exists()).toBe(true)
    })

    it('T1-F12-2: renders http_method + endpoint prefix in Model / Endpoint column', async () => {
      const items = [
        makeRealtimeItem({
          request_id: 'req-post',
          model: 'gpt-5',
          http_method: 'post',
          endpoint: '/v1/responses'
        }),
        makeRealtimeItem({
          request_id: 'req-get',
          model: 'claude-sonnet-4-5',
          http_method: 'GET',
          endpoint: '/v1/models'
        })
      ]
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse(items))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()

      const tableText = wrapper.get('table').text()
      expect(tableText).toContain('gpt-5')
      expect(tableText).toContain('POST /v1/responses')
      expect(tableText).toContain('claude-sonnet-4-5')
      expect(tableText).toContain('GET /v1/models')
    })

    it('T1-F12-3: shows only 2 Select filters initially and reveals secondary failure_reason Select on failure selection', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-fail-429',
        result: 'rejected',
        http_status: 429,
        failure_reason: 'gateway_ratelimit',
        error_message: 'Rate limit exceeded'
      })
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([failItem], {
          failure_reasons: ['gateway_ratelimit', 'client_auth_failed']
        })
      )

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      expect(mockGetRealtimeRequests).toHaveBeenCalledWith(
        { limit: 100, result: 'all' },
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
      expect(findSelectTriggers(wrapper)).toHaveLength(2)

      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()

      expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
        { limit: 100, result: 'failure' },
        expect.any(Object)
      )
      const triggersAfter = findSelectTriggers(wrapper)
      expect(triggersAfter).toHaveLength(3)
      expect(triggersAfter.some((t) => t.text().includes('全部失败原因'))).toBe(true)
    })

    it('T1-F12-4: opens real Select listbox, selects failure_reason option, and forwards failure_reason query param', async () => {
      const authItem = makeRealtimeItem({
        request_id: 'req-auth-01',
        result: 'rejected',
        http_status: 401,
        failure_reason: 'client_auth_failed',
        error_message: 'Unauthorized API key'
      })
      const ratelimitItem = makeRealtimeItem({
        request_id: 'req-rl-01',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        error_message: 'Upstream 429'
      })
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([authItem, ratelimitItem], {
          failure_reasons: ['client_auth_failed', 'upstream_rate_limit']
        })
      )

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()
      mockGetRealtimeRequests.mockClear()

      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('全部失败原因'),
        '客户端鉴权失败'
      )

      expect(mockGetRealtimeRequests).toHaveBeenCalledTimes(1)
      expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
        { limit: 100, result: 'failure', failure_reason: 'client_auth_failed' },
        expect.any(Object)
      )

      const visibleRows = wrapper.findAll('tbody tr')
      expect(visibleRows).toHaveLength(1)
      expect(visibleRows[0]!.text()).toContain('401')
    })

    it('T1-F12-5: renders buffer pill, KPI summary stats, and 6-phase breakdown bars accurately', async () => {
      const items = [
        makeRealtimeItem({
          request_id: 'req-fast',
          result: 'success',
          http_status: 200,
          total_ms: 800,
          headers_end_ms: 350
        }),
        makeRealtimeItem({
          request_id: 'req-slow-fail',
          result: 'timeout',
          http_status: 504,
          failure_reason: 'upstream_read_timeout',
          total_ms: 4200,
          headers_end_ms: null
        })
      ]
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse(items, {
          buffer_size: 2000,
          ttl_hours: 24,
          total_in_buffer: 145
        })
      )

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()

      const text = wrapper.text()
      expect(text).toContain('Redis Buffer: 145 / 2000 · TTL 24h')
      expect(text).toContain('OK 1 · Fail 1')
      expect(text).toContain('50.0%')
      expect(text).toContain('2.50 s')
      expect(text).toContain('Body Read')
      expect(text).toContain('Response Transfer')
      expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    })
  })

  // =========================================================================
  // Tier 2: Boundary & Corner Cases (>= 5 distinct cases per feature F10-F12)
  // =========================================================================

  describe('Tier 2 — F10: Boundary & Corner Cases (Formatters, Statuses, Endpoints)', () => {
    it('T2-F10-1: handles unmapped custom failure_reason and null/empty/whitespace reasons gracefully', () => {
      const zhI18n = createTestI18n('zh')
      const enI18n = createTestI18n('en')

      expect(getFailureReasonLabel('custom_edge_reason', zhI18n.global.t)).toBe('custom_edge_reason')
      expect(getFailureReasonShortLabel('custom_edge_reason', enI18n.global.t)).toBe('custom_edge_reason')

      expect(getFailureReasonLabel(null, zhI18n.global.t)).toBe('失败')
      expect(getFailureReasonLabel('   ', enI18n.global.t)).toBe('Failed')
      expect(getFailureReasonShortLabel(null, zhI18n.global.t)).toBe('')
      expect(getFailureReasonShortLabel('', enI18n.global.t)).toBe('')
    })

    it('T2-F10-2: evaluates isRequestSuccess HTTP status boundaries when result is absent', () => {
      expect(isRequestSuccess(makeRealtimeItem({ result: null, http_status: 101 }))).toBe(false)
      expect(isRequestSuccess(makeRealtimeItem({ result: null, http_status: 200 }))).toBe(true)
      expect(isRequestSuccess(makeRealtimeItem({ result: null, http_status: 299 }))).toBe(true)
      expect(isRequestSuccess(makeRealtimeItem({ result: null, http_status: 399 }))).toBe(true)

      for (const errCode of [400, 401, 403, 404, 413, 429, 499, 500, 504]) {
        expect(isRequestSuccess(makeRealtimeItem({ result: '', http_status: errCode }))).toBe(false)
      }

      expect(isRequestSuccess(makeRealtimeItem({ result: null, http_status: null, failure_reason: null }))).toBe(true)
      expect(
        isRequestSuccess(makeRealtimeItem({ result: null, http_status: null, failure_reason: 'gateway_panic' }))
      ).toBe(false)
    })

    it('T2-F10-3: formats endpoints cleanly across null method, null endpoint with endpoint_class, and all-null boundaries', () => {
      expect(formatEndpointWithMethod(null, '/v1/messages', 'messages')).toBe('/v1/messages')
      expect(formatEndpointWithMethod('  ', '/v1/messages', 'messages')).toBe('/v1/messages')
      expect(formatEndpointWithMethod('post', null, 'messages')).toBe('POST messages')
      expect(formatEndpointWithMethod('get', '', '')).toBe('GET')
      expect(formatEndpointWithMethod(null, null, null)).toBe('')
    })

    it('T2-F10-4: handles boundary durations, byte sizes, and empty 6-phase breakdown safely', () => {
      expect(formatDurationMs(null)).toBe('—')
      expect(formatDurationMs(-5)).toBe('—')
      expect(formatDurationMs(Number.NaN)).toBe('—')
      expect(formatDurationMs(0)).toBe('0 ms')
      expect(formatDurationMs(4.56)).toBe('4.6 ms')
      expect(formatDurationMs(1000)).toBe('1.00 s')

      expect(formatByteSize(null)).toBe('—')
      expect(formatByteSize(-1)).toBe('—')
      expect(formatByteSize(0)).toBe('0 B')
      expect(formatByteSize(1536)).toBe('1.5 KB')

      const emptyPhase = computePhaseBreakdown(
        makeRealtimeItem({
          body_read_ms: null,
          wait_ms: null,
          gateway_ms: null,
          upstream_ms: null,
          response_ms: null,
          finish_ms: null
        })
      )
      expect(emptyPhase.hasAnyPhase).toBe(false)
      expect(emptyPhase.sumMs).toBe(0)
      expect(emptyPhase.segments.every((s) => s.percent === 0)).toBe(true)
    })

    it('T2-F10-5: normalizes mixed-case and whitespace-padded failure_reason strings', () => {
      const zhI18n = createTestI18n('zh')
      const enI18n = createTestI18n('en')

      expect(getFailureReasonLabel('  CLIENT_AUTH_FAILED  ', zhI18n.global.t)).toBe('客户端鉴权失败')
      expect(getFailureReasonShortLabel('  BODY_TOO_LARGE\n', enI18n.global.t)).toBe('Body Too Large')
    })
  })

  describe('Tier 2 — F11: Boundary & Corner Cases (Detail Modal Banner & Clipboard)', () => {
    it('T2-F11-1: hides failure diagnostic banner for successful requests (both HTTP 200 and HTTP 101)', async () => {
      const ok200 = makeRealtimeItem({
        request_id: 'req-ok-200',
        result: 'success',
        http_status: 200,
        failure_reason: null
      })
      const ok101 = makeRealtimeItem({
        request_id: 'req-ok-101',
        result: 'success',
        http_status: 101,
        http_method: 'GET',
        endpoint: '/v1/responses',
        failure_reason: null
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([ok200, ok101]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      const rows = wrapper.findAll('tbody tr')
      await rows[0]!.trigger('click')
      await flushPromises()
      let dialog = findDialog(wrapper)
      expect(findFailureBannerSection(dialog, 'zh')).toBeUndefined()
      await dialog.get('button[aria-label="Close modal"]').trigger('click')
      await flushPromises()

      await rows[1]!.trigger('click')
      await flushPromises()
      dialog = findDialog(wrapper)
      expect(findFailureBannerSection(dialog, 'zh')).toBeUndefined()
      expect(dialog.text()).toContain('101')
    })

    it('T2-F11-2: shows empty error message fallback and omits error copy button when error_message is null or whitespace', async () => {
      const noErrItem = makeRealtimeItem({
        request_id: 'req-empty-errmsg',
        result: 'timeout',
        http_status: 504,
        failure_reason: 'upstream_read_timeout',
        error_message: '   '
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([noErrItem]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      expect(banner.text()).toContain('暂无详细错误信息')
      expect(banner.findAll('button').filter((b) => b.text().trim() === '复制')).toHaveLength(0)
    })

    it('T2-F11-3: renders and copies >512-char error_message with special characters verbatim without corruption', async () => {
      const specialChunk = `<script>alert("xss")</script> & token="sk-***" \nline2: 'quote' `
      const longError = specialChunk.repeat(10).slice(0, 560)
      const longItem = makeRealtimeItem({
        request_id: 'req-long-errmsg',
        result: 'upstream_error',
        http_status: 502,
        failure_reason: 'upstream_server_error',
        error_message: longError
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([longItem]))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'en')!
      const pre = banner.find('pre')
      expect(pre.exists()).toBe(true)
      expect(pre.text()).toBe(longError.trim())

      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === 'Copy')!
      await copyBtn.trigger('click')
      await flushPromises()
      expect(clipboardWriteMock).toHaveBeenCalledWith(longError.trim())
    })

    it('T2-F11-4: omits optional upstream_status/code/http_method chips cleanly when null and handles unmapped failure_reason', async () => {
      const sparseFail = makeRealtimeItem({
        request_id: 'req-sparse-fail',
        http_method: null,
        endpoint: '/v1/messages',
        result: 'gateway_error',
        http_status: 500,
        failure_reason: 'custom_edge_reason',
        upstream_status: null,
        upstream_error_code: null,
        attempt_count: null,
        failed_attempts: null,
        error_message: null
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([sparseFail]))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'en')!
      const bannerText = banner.text()

      expect(bannerText).toContain('custom_edge_reason')
      expect(bannerText).not.toContain('Upstream Status')
      expect(bannerText).not.toContain('Upstream Error Code')
      expect(bannerText).not.toContain('null')
      expect(bannerText).not.toContain('undefined')
    })

    it('T2-F11-5: triggers appStore warning toast gracefully when both navigator.clipboard and execCommand fail', async () => {
      setSecureContextAndClipboard(true, async () => {
        throw new Error('Clipboard permission denied')
      })
      Object.defineProperty(document, 'execCommand', {
        configurable: true,
        value: vi.fn().mockReturnValue(false)
      })

      const failItem = makeRealtimeItem({
        request_id: 'req-clip-fail',
        result: 'rejected',
        http_status: 403,
        failure_reason: 'client_forbidden',
        error_message: 'Model access forbidden'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper, appStore } = mountRealtimeCard('zh')
      await flushPromises()
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === '复制')!
      await copyBtn.trigger('click')
      await flushPromises()

      expect(appStore.toasts.some((toast) => toast.type === 'warning' && toast.message === '复制失败')).toBe(true)
    })
  })

  describe('Tier 2 — F12: Boundary & Corner Cases (List Card Filter Reset, Empty, Disabled, Error)', () => {
    it('T2-F12-1: resets failure_reason and hides secondary Select in a single API call when switching away from failure', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-reset-01',
        result: 'rejected',
        http_status: 401,
        failure_reason: 'client_auth_failed',
        error_message: 'Invalid API key'
      })
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([failItem], { failure_reasons: ['client_auth_failed'] })
      )

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()

      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('全部失败原因'),
        '客户端鉴权失败'
      )
      expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
        { limit: 100, result: 'failure', failure_reason: 'client_auth_failed' },
        expect.any(Object)
      )

      mockGetRealtimeRequests.mockClear()
      await findSegmentedButton(wrapper, '全部').trigger('click')
      await flushPromises()

      expect(mockGetRealtimeRequests).toHaveBeenCalledTimes(1)
      expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
        { limit: 100, result: 'all' },
        expect.any(Object)
      )
      expect(findSelectTriggers(wrapper)).toHaveLength(2)
    })

    it('T2-F12-2: handles empty failure_reasons response array and shows EmptyState when filter yields zero matches', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-only-timeout',
        result: 'timeout',
        http_status: 504,
        failure_reason: 'upstream_read_timeout'
      })
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([failItem], { failure_reasons: [] })
      )

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()

      // Even when backend returns empty failure_reasons, canonical + item options populate the selector
      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('全部失败原因'),
        '访问被拒绝'
      )

      expect(wrapper.text()).toContain('暂无匹配的实时请求记录')
      expect(wrapper.find('tbody tr').exists()).toBe(false)
    })

    it('T2-F12-3: renders disabled banner, hides filter bar, and stops 5s auto-refresh when enabled is false', async () => {
      vi.useFakeTimers()
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([], {
          enabled: false,
          buffer_size: 0,
          ttl_hours: 0,
          total_in_buffer: 0
        })
      )

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      expect(wrapper.text()).toContain('实时请求采样未启用')
      expect(wrapper.text()).toContain('MONITORING_OPS_SAMPLES_ENABLED=true')
      expect(wrapper.find('[role="group"]').exists()).toBe(false)

      mockGetRealtimeRequests.mockClear()
      vi.advanceTimersByTime(10000)
      await flushPromises()
      expect(mockGetRealtimeRequests).not.toHaveBeenCalled()
    })

    it('T2-F12-4: surfaces API rejection error banner cleanly and recovers on manual refresh', async () => {
      mockGetRealtimeRequests.mockRejectedValueOnce(new Error('Gateway 503 unavailable'))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()

      expect(wrapper.text()).toContain('Gateway 503 unavailable')

      mockGetRealtimeRequests.mockResolvedValueOnce(
        makeRealtimeResponse([
          makeRealtimeItem({
            request_id: 'req-recovered-01',
            http_method: 'POST',
            endpoint: '/v1/responses'
          })
        ])
      )

      const refreshBtn = wrapper.findAll('button').find((b) => b.text().trim() === 'Refresh')!
      await refreshBtn.trigger('click')
      await flushPromises()

      expect(wrapper.text()).not.toContain('Gateway 503 unavailable')
      expect(wrapper.text()).toContain('POST /v1/responses')
    })

    it('T2-F12-5: omits null strings when http_method/endpoint/error_message are null and falls back status tooltip cleanly', async () => {
      const nullRichFail = makeRealtimeItem({
        request_id: 'req-null-fields',
        http_method: null,
        endpoint: null,
        endpoint_class: 'messages',
        result: 'gateway_error',
        http_status: 500,
        failure_reason: 'gateway_db',
        error_message: null
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([nullRichFail]))

      const { wrapper } = mountRealtimeCard('zh')
      await flushPromises()

      const rowText = wrapper.get('tbody tr').text()
      expect(rowText).toContain('messages')
      expect(rowText).not.toContain('null')
      expect(rowText).toContain('网关 DB')

      const titledEl = wrapper.find('[title*="gateway_db"]')
      expect(titledEl.exists()).toBe(true)
      expect(titledEl.attributes('title')).toContain('网关数据库错误')
    })
  })

  // =========================================================================
  // Tier 3: Pairwise Cross-Feature Combinations (>= 4 combinations)
  // =========================================================================

  describe('Tier 3 — Cross-Feature Pairwise Combinations (F10 x F11 x F12)', () => {
    it('T3-1: zh full filter -> secondary failure_reason -> 300ms debounced search -> row click -> banner copy -> close modal', async () => {
      vi.useFakeTimers()

      const targetItem = makeRealtimeItem({
        request_id: 'req-up-429-target',
        user_email: 'oncall@sub2api.io',
        http_method: 'POST',
        endpoint: '/v1/responses',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        upstream_status: 429,
        upstream_error_code: 'rate_limit_exceeded',
        attempt_count: 3,
        failed_attempts: 3,
        error_message: 'Upstream returned 429: RPM quota exhausted on account pool-east'
      })
      const otherFail = makeRealtimeItem({
        request_id: 'req-auth-401-other',
        result: 'rejected',
        http_status: 401,
        failure_reason: 'client_auth_failed',
        error_message: 'Invalid API key'
      })

      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([targetItem, otherFail], {
          failure_reasons: ['client_auth_failed', 'upstream_rate_limit']
        })
      )

      const { wrapper, appStore } = mountRealtimeCard('zh')
      await flushPromises()

      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()

      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('全部失败原因'),
        '上游限流'
      )

      mockGetRealtimeRequests.mockClear()
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([targetItem], {
          failure_reasons: ['client_auth_failed', 'upstream_rate_limit']
        })
      )

      const searchInput = wrapper.get('input[type="text"]')
      await searchInput.setValue('req-up-429')
      vi.advanceTimersByTime(250)
      await flushPromises()
      expect(mockGetRealtimeRequests).not.toHaveBeenCalled()

      vi.advanceTimersByTime(60)
      await flushPromises()
      expect(mockGetRealtimeRequests).toHaveBeenCalledWith(
        {
          limit: 100,
          result: 'failure',
          failure_reason: 'upstream_rate_limit',
          search: 'req-up-429'
        },
        expect.any(Object)
      )

      const row = wrapper.get('tbody tr')
      expect(row.find(`[title="${targetItem.error_message}"]`).exists()).toBe(true)

      await row.trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      expect(banner.text()).toContain('上游限流')
      expect(banner.text()).toContain('upstream_rate_limit')
      expect(banner.text()).toContain('rate_limit_exceeded')

      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === '复制')!
      await copyBtn.trigger('click')
      await flushPromises()

      expect(clipboardWriteMock).toHaveBeenCalledWith(targetItem.error_message)
      expect(appStore.toasts.some((toast) => toast.type === 'success' && toast.message === '已复制')).toBe(true)

      await dialog.get('button[aria-label="Close modal"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    })

    it('T3-2: reactive zh <-> en locale switching updates both background list and open detail modal with zero CJK leakage in en', async () => {
      const failItem = makeRealtimeItem({
        request_id: 'req-bilingual-01',
        http_method: 'POST',
        endpoint: '/v1/responses',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        upstream_status: 429,
        upstream_error_code: 'rate_limit_exceeded',
        error_message: 'Upstream quota exceeded'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([failItem]))

      const { wrapper, i18n } = mountRealtimeCard('zh')
      await flushPromises()

      expect(wrapper.text()).toContain('实时请求流')
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      let dialog = findDialog(wrapper)
      expect(dialog.text()).toContain('失败原因与错误详情')
      expect(dialog.text()).toContain('上游限流')

      i18n.global.locale.value = 'en'
      singletonI18n.global.locale.value = 'en'
      await nextTick()
      await flushPromises()

      expect(wrapper.text()).toContain('Realtime Request Stream')
      expect(wrapper.text()).not.toMatch(CJK_REGEX)
      expect(wrapper.text()).not.toMatch(RAW_I18N_KEY_REGEX)

      dialog = findDialog(wrapper)
      expect(dialog.text()).toContain('Failure Reason & Error Details')
      expect(dialog.text()).toContain('Upstream Rate Limit')
      expect(dialog.text()).not.toMatch(CJK_REGEX)

      i18n.global.locale.value = 'zh'
      singletonI18n.global.locale.value = 'zh'
      await nextTick()
      await flushPromises()
      dialog = findDialog(wrapper)
      expect(dialog.text()).toContain('失败原因与错误详情')
      expect(dialog.text()).toContain('上游限流')
    })

    it('T3-3: verifies complete 400/401/403/404/413/429/499 enum matrix across list short labels and modal banners in zh and en', async () => {
      const matrixItems = R2_4XX_MATRIX.map((entry, idx) =>
        makeRealtimeItem({
          request_id: `req-matrix-${entry.httpStatus}-${idx}`,
          http_method: entry.method,
          endpoint: entry.endpoint,
          result: entry.result,
          http_status: entry.httpStatus,
          failure_reason: entry.failureReason,
          error_message: entry.errorMessage
        })
      )
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse(matrixItems))

      for (const locale of ['zh', 'en'] as const) {
        const { wrapper, i18n } = mountRealtimeCard(locale)
        await flushPromises()

        const rows = wrapper.findAll('tbody tr')
        expect(rows).toHaveLength(R2_4XX_MATRIX.length)

        for (let i = 0; i < R2_4XX_MATRIX.length; i += 1) {
          const spec = R2_4XX_MATRIX[i]!
          const expectedShort = getFailureReasonShortLabel(spec.failureReason, i18n.global.t)
          const expectedFull = getFailureReasonLabel(spec.failureReason, i18n.global.t)

          const rowText = rows[i]!.text()
          expect(rowText).toContain(`${spec.method} ${spec.endpoint}`)
          expect(rowText).toContain(String(spec.httpStatus))
          expect(rowText).toContain(expectedShort)
          expect(
            rows[i]!.findAll('[title]').some((el) => el.attributes('title') === spec.errorMessage)
          ).toBe(true)

          await rows[i]!.trigger('click')
          await flushPromises()

          const dialog = findDialog(wrapper)
          const banner = findFailureBannerSection(dialog, locale)!
          expect(banner.text()).toContain(expectedFull)
          expect(banner.text()).toContain(spec.failureReason)
          expect(banner.text()).toContain(spec.errorMessage)

          await dialog.get('button[aria-label="Close modal"]').trigger('click')
          await flushPromises()
        }

        if (locale === 'en') {
          expect(wrapper.text()).not.toMatch(CJK_REGEX)
        }
      }
    })

    it('T3-4: maintains http_method, upstream_status, upstream_error_code & attempt consistency between list and modal', async () => {
      const item = makeRealtimeItem({
        request_id: 'req-consistency-502',
        http_method: 'PUT',
        endpoint: '/v1/files/upload',
        endpoint_class: 'files',
        result: 'upstream_error',
        http_status: 502,
        failure_reason: 'upstream_server_error',
        upstream_status: 503,
        upstream_error_code: 'backend_overloaded',
        attempt_count: 4,
        failed_attempts: 4,
        error_message: 'Upstream returned 503 backend_overloaded'
      })
      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([item]))

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()

      expect(wrapper.get('tbody tr').text()).toContain('PUT /v1/files/upload')
      await wrapper.get('tbody tr').trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'en')!
      const basicInfo = findBasicInfoSection(dialog, 'en')!

      expect(banner.text()).toContain('503')
      expect(banner.text()).toContain('backend_overloaded')
      expect(banner.text()).toContain('3') // retries = 4 - 1
      expect(banner.text()).toContain('4')
      expect(basicInfo.text()).toContain('PUT')
      expect(basicInfo.text()).toContain('/v1/files/upload')
    })

    it('T3-5: combines model + limit + secondary failure_reason filters and preserves them across 5s auto-refresh polling', async () => {
      vi.useFakeTimers()

      const item = makeRealtimeItem({
        request_id: 'req-poll-403',
        model: 'claude-sonnet-4-5',
        http_method: 'POST',
        endpoint: '/v1/messages',
        result: 'rejected',
        http_status: 403,
        failure_reason: 'client_forbidden',
        error_message: 'IP not in allowlist'
      })
      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([item], {
          models: ['claude-sonnet-4-5', 'gpt-5'],
          failure_reasons: ['client_forbidden', 'client_auth_failed']
        })
      )

      const { wrapper } = mountRealtimeCard('en')
      await flushPromises()

      await findSegmentedButton(wrapper, 'Failure').trigger('click')
      await flushPromises()

      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('All Failure Reasons'),
        'Forbidden'
      )
      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('All Models'),
        'claude-sonnet-4-5'
      )
      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('Latest 100'),
        'Latest 200'
      )

      mockGetRealtimeRequests.mockClear()
      vi.advanceTimersByTime(5000)
      await flushPromises()

      expect(mockGetRealtimeRequests).toHaveBeenCalledTimes(1)
      expect(mockGetRealtimeRequests).toHaveBeenCalledWith(
        {
          limit: 200,
          result: 'failure',
          failure_reason: 'client_forbidden',
          model: 'claude-sonnet-4-5'
        },
        expect.any(Object)
      )
    })
  })

  // =========================================================================
  // Tier 4: Real-World Operator Troubleshooting Scenarios (S5, S6, S7)
  // =========================================================================

  describe('Tier 4 — Real-World Operator Troubleshooting Scenarios (S5-S7)', () => {
    it('S5: On-call operator rate-limit spike triage in zh (filter -> secondary reason -> inspect banner -> copy -> clear)', async () => {
      const okReq = makeRealtimeItem({
        request_id: 'req-s5-ok',
        result: 'success',
        http_status: 200,
        total_ms: 450
      })
      const gw429 = makeRealtimeItem({
        request_id: 'req-s5-gw-429',
        user_email: 'tenant-a@corp.com',
        http_method: 'POST',
        endpoint: '/v1/responses',
        result: 'rejected',
        http_status: 429,
        failure_reason: 'gateway_ratelimit',
        attempt_count: 1,
        failed_attempts: 1,
        total_ms: 15,
        error_message: 'Gateway concurrency limit reached for key sk-***tenantA'
      })
      const up429 = makeRealtimeItem({
        request_id: 'req-s5-up-429',
        user_email: 'tenant-b@corp.com',
        http_method: 'POST',
        endpoint: '/v1/messages',
        result: 'upstream_error',
        http_status: 429,
        failure_reason: 'upstream_rate_limit',
        upstream_status: 429,
        upstream_error_code: 'rate_limit_exceeded',
        attempt_count: 3,
        failed_attempts: 3,
        total_ms: 3400,
        error_message: 'Anthropic upstream 429: tokens_per_min exceeded'
      })

      mockGetRealtimeRequests.mockResolvedValue(
        makeRealtimeResponse([okReq, gw429, up429], {
          failure_reasons: ['gateway_ratelimit', 'upstream_rate_limit']
        })
      )

      const { wrapper, appStore } = mountRealtimeCard('zh')
      await flushPromises()

      // SLA drop visible in KPI (< 95% -> red)
      expect(wrapper.text()).toContain('33.3%')

      // Operator narrows to failures, then upstream_rate_limit
      await findSegmentedButton(wrapper, '失败').trigger('click')
      await flushPromises()
      await openSelectAndChooseOption(
        wrapper,
        (text) => text.includes('全部失败原因'),
        '上游限流'
      )

      const filteredRows = wrapper.findAll('tbody tr')
      expect(filteredRows).toHaveLength(1)
      expect(filteredRows[0]!.text()).toContain('tenant-b@corp.com')

      // Open modal and inspect full diagnostic banner
      await filteredRows[0]!.trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      expect(banner.text()).toContain('上游限流')
      expect(banner.text()).toContain('upstream_rate_limit')
      expect(banner.text()).toContain('rate_limit_exceeded')
      expect(banner.text()).toContain('Anthropic upstream 429: tokens_per_min exceeded')

      // Copy error for incident ticket
      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === '复制')!
      await copyBtn.trigger('click')
      await flushPromises()
      expect(clipboardWriteMock).toHaveBeenCalledWith('Anthropic upstream 429: tokens_per_min exceeded')
      expect(appStore.toasts.some((toast) => toast.type === 'success')).toBe(true)

      // Close modal and reset back to All
      await dialog.get('button[aria-label="Close modal"]').trigger('click')
      await flushPromises()
      await findSegmentedButton(wrapper, '全部').trigger('click')
      await flushPromises()
      expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    })

    it('S6: On-call client config rejection triage & bilingual zh <-> en handover', async () => {
      vi.useFakeTimers()

      const auth401 = makeRealtimeItem({
        request_id: 'req-s6-auth-401',
        client_request_id: 'cli-cfg-401',
        http_method: 'POST',
        endpoint: '/v1/messages',
        result: 'rejected',
        http_status: 401,
        failure_reason: 'client_auth_failed',
        error_message: 'API key missing or malformed'
      })
      const body413 = makeRealtimeItem({
        request_id: 'req-s6-body-413',
        client_request_id: 'cli-cfg-413',
        http_method: 'POST',
        endpoint: '/v1/responses',
        result: 'rejected',
        http_status: 413,
        failure_reason: 'body_too_large',
        error_message: 'Payload 14.2MB exceeds 10MB gateway cap'
      })

      mockGetRealtimeRequests.mockResolvedValue(makeRealtimeResponse([auth401, body413]))

      const { wrapper, i18n } = mountRealtimeCard('zh')
      await flushPromises()

      // Search specific client_request_id
      mockGetRealtimeRequests.mockResolvedValueOnce(makeRealtimeResponse([body413]))
      await wrapper.get('input[type="text"]').setValue('cli-cfg-413')
      vi.advanceTimersByTime(300)
      await flushPromises()

      expect(mockGetRealtimeRequests).toHaveBeenLastCalledWith(
        { limit: 100, result: 'all', search: 'cli-cfg-413' },
        expect.any(Object)
      )

      const row = wrapper.get('tbody tr')
      expect(row.text()).toContain('请求体过大')
      expect(row.text()).toContain('POST /v1/responses')

      await row.trigger('click')
      await flushPromises()

      // Handover to English-speaking SRE while modal is open
      i18n.global.locale.value = 'en'
      singletonI18n.global.locale.value = 'en'
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'en')!
      expect(banner.text()).toContain('Payload Too Large')
      expect(banner.text()).toContain('body_too_large')
      expect(banner.text()).toContain('Payload 14.2MB exceeds 10MB gateway cap')
      expect(wrapper.text()).toContain('Body Too Large')
      expect(wrapper.text()).not.toMatch(CJK_REGEX)
      expect(wrapper.text()).not.toMatch(RAW_I18N_KEY_REGEX)
    })

    it('S7: Mobile viewport & degraded-state ops console triage (API recovery + mobile cards + clipboard warning fallback)', async () => {
      setViewportMatches(false)
      setSecureContextAndClipboard(false, async () => {
        throw new Error('Insecure context')
      })
      Object.defineProperty(document, 'execCommand', {
        configurable: true,
        value: vi.fn().mockReturnValue(false)
      })

      const mobileFailItem = makeRealtimeItem({
        request_id: 'req-s7-mobile-403',
        http_method: 'POST',
        endpoint: '/v1/responses',
        result: 'rejected',
        http_status: 403,
        failure_reason: 'client_forbidden',
        error_message: 'Model tier restricted for current subscription'
      })

      // First API call fails; operator manually refreshes to recover
      mockGetRealtimeRequests
        .mockRejectedValueOnce(new Error('Temporary Redis read timeout'))
        .mockResolvedValueOnce(makeRealtimeResponse([mobileFailItem]))

      const { wrapper, appStore } = mountRealtimeCard('zh')
      await flushPromises()

      expect(wrapper.text()).toContain('Temporary Redis read timeout')

      const refreshBtn = wrapper.findAll('button').find((b) => b.text().trim() === '刷新')!
      await refreshBtn.trigger('click')
      await flushPromises()

      // Desktop table must not render on mobile viewport
      expect(wrapper.find('table').exists()).toBe(false)
      expect(wrapper.text()).toContain('POST /v1/responses')
      expect(wrapper.text()).toContain('禁止访问')
      expect(
        wrapper.find('[title="Model tier restricted for current subscription"]').exists()
      ).toBe(true)

      // Tap mobile card detail button to open modal
      const detailBtn = wrapper.findAll('button').find((b) => b.text().trim() === '详情')!
      await detailBtn.trigger('click')
      await flushPromises()

      const dialog = findDialog(wrapper)
      const banner = findFailureBannerSection(dialog, 'zh')!
      expect(banner.text()).toContain('访问被拒绝')
      expect(banner.text()).toContain('client_forbidden')

      // Attempt copy under degraded insecure context -> shows warning toast cleanly
      const copyBtn = banner.findAll('button').find((b) => b.text().trim() === '复制')!
      await copyBtn.trigger('click')
      await flushPromises()

      expect(appStore.toasts.some((toast) => toast.type === 'warning' && toast.message === '复制失败')).toBe(true)
    })
  })
})
