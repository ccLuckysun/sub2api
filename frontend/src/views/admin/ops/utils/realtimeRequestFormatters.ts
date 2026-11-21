import type { OpsRealtimeRequestItem } from '@/api/admin/ops'

export type RealtimePhaseField =
  | 'body_read_ms'
  | 'wait_ms'
  | 'gateway_ms'
  | 'upstream_ms'
  | 'response_ms'
  | 'finish_ms'

export interface RealtimePhaseMeta {
  key: 'bodyRead' | 'wait' | 'gateway' | 'upstream' | 'response' | 'finish'
  field: RealtimePhaseField
  color: string
  labelKey: string
}

export const REALTIME_REQUEST_PHASES: readonly RealtimePhaseMeta[] = [
  {
    key: 'bodyRead',
    field: 'body_read_ms',
    color: '#3987e5',
    labelKey: 'admin.ops.realtimeRequests.phases.bodyRead'
  },
  {
    key: 'wait',
    field: 'wait_ms',
    color: '#1aa57a',
    labelKey: 'admin.ops.realtimeRequests.phases.wait'
  },
  {
    key: 'gateway',
    field: 'gateway_ms',
    color: '#e07a3f',
    labelKey: 'admin.ops.realtimeRequests.phases.gateway'
  },
  {
    key: 'upstream',
    field: 'upstream_ms',
    color: '#d4a72c',
    labelKey: 'admin.ops.realtimeRequests.phases.upstream'
  },
  {
    key: 'response',
    field: 'response_ms',
    color: '#d9669a',
    labelKey: 'admin.ops.realtimeRequests.phases.response'
  },
  {
    key: 'finish',
    field: 'finish_ms',
    color: '#9085e9',
    labelKey: 'admin.ops.realtimeRequests.phases.finish'
  }
] as const

export interface RealtimePhaseSegment extends RealtimePhaseMeta {
  valueMs: number | null
  effectiveMs: number
  percent: number
}

export interface RealtimePhaseBreakdown {
  segments: RealtimePhaseSegment[]
  sumMs: number
  hasAnyPhase: boolean
}

const KNOWN_FAILURE_REASON_KEYS = new Set([
  // Canonical backend ClassifyFailure reasons (backend/internal/monitoring/ops_labels.go)
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
  // Structured result fallbacks
  'client_abort',
  'gateway_error',
  'rejected',
  // Legacy / generic aliases
  'client_canceled',
  'client_disconnect',
  'context_canceled',
  'context_deadline_exceeded',
  'timeout',
  'upstream_timeout',
  'upstream_error',
  'upstream_5xx',
  'upstream_4xx',
  'rate_limited',
  'upstream_rate_limited',
  'overloaded',
  'upstream_overloaded',
  'no_available_account',
  'no_available_accounts',
  'account_unavailable',
  'invalid_api_key',
  'unauthorized',
  'forbidden',
  'insufficient_balance',
  'quota_exceeded',
  'concurrency_limit',
  'wait_timeout',
  'bad_request',
  'body_read_error',
  'network_error',
  'internal_error',
  'unknown'
])

export function formatDurationMs(ms?: number | null): string {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)} s`
  }
  if (ms > 0 && ms < 10 && !Number.isInteger(ms)) {
    return `${ms.toFixed(1)} ms`
  }
  return `${Math.round(ms)} ms`
}

export function formatByteSize(bytes?: number | null): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes === 0) return '0 B'
  const kb = 1024
  const mb = kb * 1024
  const gb = mb * 1024
  if (bytes < kb) return `${Math.round(bytes)} B`
  if (bytes < mb) return `${(bytes / kb).toFixed(1)} KB`
  if (bytes < gb) return `${(bytes / mb).toFixed(2)} MB`
  return `${(bytes / gb).toFixed(2)} GB`
}

export function formatObservedTime(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  const ss = String(d.getSeconds()).padStart(2, '0')
  const ms = String(d.getMilliseconds()).padStart(3, '0')
  return `${hh}:${mm}:${ss}.${ms}`
}

export function formatObservedDateTimeFull(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const yyyy = d.getFullYear()
  const mo = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${yyyy}-${mo}-${dd} ${formatObservedTime(iso)}`
}

export function isRequestSuccess(item: OpsRealtimeRequestItem): boolean {
  const res = String(item.result || '').trim().toLowerCase()
  if (res) {
    // Match backend opsSampleIsSuccess semantics: any explicit non-success result
    // (upstream_error, timeout, client_abort, gateway_error, rejected, etc.) is failure,
    // even when HTTP 200 headers were already sent before a streaming error.
    return res === 'success' || res === 'ok'
  }
  if (typeof item.http_status === 'number') {
    return item.http_status >= 200 && item.http_status < 400
  }
  return !item.failure_reason
}

export function computePhaseBreakdown(item: OpsRealtimeRequestItem): RealtimePhaseBreakdown {
  let sumMs = 0
  let hasAnyPhase = false

  const rawValues = REALTIME_REQUEST_PHASES.map((phase) => {
    const val = item[phase.field]
    const valid = typeof val === 'number' && Number.isFinite(val) && val >= 0
    if (valid) {
      hasAnyPhase = true
      sumMs += val
    }
    return { phase, valueMs: valid ? val : null, effectiveMs: valid ? val : 0 }
  })

  const denominator = sumMs > 0 ? sumMs : 0
  const segments: RealtimePhaseSegment[] = rawValues.map(({ phase, valueMs, effectiveMs }) => ({
    ...phase,
    valueMs,
    effectiveMs,
    percent: denominator > 0 ? (effectiveMs / denominator) * 100 : 0
  }))

  return { segments, sumMs, hasAnyPhase }
}

export function getFailureReasonLabel(
  reason: string | null | undefined,
  t: (key: string) => string
): string {
  const normalized = String(reason || '').trim().toLowerCase()
  if (!normalized) return t('admin.ops.realtimeRequests.status.failure')
  if (KNOWN_FAILURE_REASON_KEYS.has(normalized)) {
    return t(`admin.ops.realtimeRequests.failureReasons.${normalized}`)
  }
  return String(reason).trim()
}

export function getByteStatusBadge(
  status: string | null | undefined,
  t: (key: string) => string
): { label: string; badgeClass: string } {
  const normalized = String(status || '').trim().toLowerCase()
  if (normalized === 'complete') {
    return {
      label: t('admin.ops.realtimeRequests.byteStatus.complete'),
      badgeClass: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    }
  }
  if (normalized === 'partial') {
    return {
      label: t('admin.ops.realtimeRequests.byteStatus.partial'),
      badgeClass: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    }
  }
  return {
    label: t('admin.ops.realtimeRequests.byteStatus.unknown'),
    badgeClass: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
}

export function getHttpStatusClass(status?: number | null): string {
  if (typeof status !== 'number' || !Number.isFinite(status)) {
    return 'bg-gray-100 text-gray-700 ring-gray-600/20 dark:bg-dark-700 dark:text-gray-300 dark:ring-gray-500/30'
  }
  if (status >= 200 && status < 300) {
    return 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-300 dark:ring-emerald-500/30'
  }
  if (status >= 400 && status < 500) {
    return 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-300 dark:ring-amber-500/30'
  }
  if (status >= 500) {
    return 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-300 dark:ring-red-500/30'
  }
  return 'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-300 dark:ring-blue-500/30'
}
