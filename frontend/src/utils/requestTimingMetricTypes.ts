/**
 * 请求耗时告警指标的单一事实来源。
 *
 * 放在 utils 而不是 api 模块，是因为告警规则表单（组件）与 API 客户端都要用，
 * 组件不应为了一个字符串白名单而去依赖整个 HTTP 客户端（也避免测试里连带 mock）。
 */

export const REQUEST_TIMING_METRIC_TYPES = [
  'upload_ms_p90',
  'upload_kbps_p50',
  'large_body_ratio',
  'real_ttfb_ms_p95',
] as const

export type RequestTimingMetricType = (typeof REQUEST_TIMING_METRIC_TYPES)[number]

export const isRequestTimingMetric = (metric: string | null | undefined): boolean =>
  !!metric && (REQUEST_TIMING_METRIC_TYPES as readonly string[]).includes(metric)

/** 后端 422 预算保护错误码；出现时 UI 提示缩短时间范围，不静默空图。 */
export const REQUEST_TIMING_BUDGET_EXCEEDED = 'REQUEST_TIMING_BUDGET_EXCEEDED'

/**
 * 后端把预算错误码放在 `reason`（历史实现也可能放在 `code`），且 axios 会再包一层
 * `response.data`；这里把四种位置都认一遍，避免 UI 把 422 预算提示降级成英文原始错误。
 */
export const isRequestTimingBudgetExceeded = (error: unknown): boolean => {
  const e = error as {
    code?: unknown
    reason?: unknown
    response?: { status?: number; data?: { code?: unknown; reason?: unknown } }
  } | null
  if (!e) return false
  const markers = [e.reason, e.code, e.response?.data?.reason, e.response?.data?.code]
  return markers.some((marker) => marker === REQUEST_TIMING_BUDGET_EXCEEDED)
}
