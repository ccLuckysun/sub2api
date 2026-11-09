import { describe, expect, it } from 'vitest'
import {
  REQUEST_TIMING_BUDGET_EXCEEDED,
  REQUEST_TIMING_METRIC_TYPES,
  isRequestTimingBudgetExceeded,
  isRequestTimingMetric,
} from '@/utils/requestTimingMetricTypes'

describe('requestTimingMetricTypes', () => {
  it('lists exactly the four request-timing alert metrics', () => {
    expect([...REQUEST_TIMING_METRIC_TYPES]).toEqual([
      'upload_ms_p90',
      'upload_kbps_p50',
      'large_body_ratio',
      'real_ttfb_ms_p95',
    ])
    expect(isRequestTimingMetric('upload_ms_p90')).toBe(true)
    expect(isRequestTimingMetric('p95_latency_ms')).toBe(false)
    expect(isRequestTimingMetric(null)).toBe(false)
  })

  describe('isRequestTimingBudgetExceeded', () => {
    // 后端把预算标记放在 `reason`（实测 422 体：{code:422, message, reason:"REQUEST_TIMING_BUDGET_EXCEEDED"}）；
    // 只认 code 会让 7d 长窗口的分组卡退化成英文原始错误。
    it('detects the reason marker the backend actually sends', () => {
      expect(isRequestTimingBudgetExceeded({ response: { status: 422, data: { code: 422, reason: REQUEST_TIMING_BUDGET_EXCEEDED } } })).toBe(true)
      expect(isRequestTimingBudgetExceeded({ reason: REQUEST_TIMING_BUDGET_EXCEEDED })).toBe(true)
    })

    it('still detects a bare code marker for forward compatibility', () => {
      expect(isRequestTimingBudgetExceeded({ code: REQUEST_TIMING_BUDGET_EXCEEDED })).toBe(true)
      expect(isRequestTimingBudgetExceeded({ response: { data: { code: REQUEST_TIMING_BUDGET_EXCEEDED } } })).toBe(true)
    })

    it('ignores unrelated failures', () => {
      expect(isRequestTimingBudgetExceeded(null)).toBe(false)
      expect(isRequestTimingBudgetExceeded(undefined)).toBe(false)
      expect(isRequestTimingBudgetExceeded({ code: 'ERR_CANCELED', name: 'AbortError' })).toBe(false)
      expect(isRequestTimingBudgetExceeded({ response: { status: 500, data: { message: 'boom' } } })).toBe(false)
      expect(isRequestTimingBudgetExceeded({ response: { status: 422, data: { reason: 'SOMETHING_ELSE' } } })).toBe(false)
    })
  })
})
