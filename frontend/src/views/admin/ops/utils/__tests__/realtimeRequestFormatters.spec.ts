import { describe, expect, it } from 'vitest'
import {
  CANONICAL_FAILURE_REASON_KEYS,
  formatEndpointWithMethod,
  getFailureReasonLabel,
  getFailureReasonShortLabel,
  isRequestSuccess
} from '../realtimeRequestFormatters'

const identityT = (key: string) => key

describe('realtimeRequestFormatters', () => {
  describe('formatEndpointWithMethod', () => {
    it('formats uppercase method and endpoint or endpointClass', () => {
      expect(formatEndpointWithMethod('POST', '/v1/responses', 'responses')).toBe('POST /v1/responses')
      expect(formatEndpointWithMethod('get', '/v1/models', 'models')).toBe('GET /v1/models')
      expect(formatEndpointWithMethod('post', null, 'messages')).toBe('POST messages')
      expect(formatEndpointWithMethod('get', '   ', 'realtime')).toBe('GET realtime')
    })

    it('handles missing method, missing endpoint, and all-empty inputs cleanly', () => {
      expect(formatEndpointWithMethod(null, '/v1/messages', 'messages')).toBe('/v1/messages')
      expect(formatEndpointWithMethod(undefined, null, 'messages')).toBe('messages')
      expect(formatEndpointWithMethod('GET', null, null)).toBe('GET')
      expect(formatEndpointWithMethod(null, null, null)).toBe('')
      expect(formatEndpointWithMethod('  ', '  ', undefined)).toBe('')
    })
  })

  describe('getFailureReasonLabel and getFailureReasonShortLabel', () => {
    it('maps all canonical and R2 4xx failure reasons', () => {
      const r2Reasons = [
        'client_bad_request',
        'client_auth_failed',
        'client_forbidden',
        'client_not_found',
        'body_too_large',
        'gateway_ratelimit',
        'client_abort'
      ]

      for (const reason of r2Reasons) {
        expect(CANONICAL_FAILURE_REASON_KEYS).toContain(reason)
        expect(getFailureReasonLabel(reason, identityT)).toBe(
          `admin.ops.realtimeRequests.failureReasons.${reason}`
        )
        expect(getFailureReasonShortLabel(reason, identityT)).toBe(
          `admin.ops.realtimeRequests.failureReasonsShort.${reason}`
        )
      }
    })

    it('normalizes whitespace and casing, falls back for legacy/unknown reasons, and handles empty inputs', () => {
      expect(getFailureReasonLabel('  CLIENT_AUTH_FAILED ', identityT)).toBe(
        'admin.ops.realtimeRequests.failureReasons.client_auth_failed'
      )
      expect(getFailureReasonShortLabel('  CLIENT_AUTH_FAILED ', identityT)).toBe(
        'admin.ops.realtimeRequests.failureReasonsShort.client_auth_failed'
      )

      // Legacy alias falls back to getFailureReasonLabel
      expect(getFailureReasonShortLabel('upstream_timeout', identityT)).toBe(
        'admin.ops.realtimeRequests.failureReasons.upstream_timeout'
      )

      // Unknown raw string passes through trimmed
      expect(getFailureReasonLabel('  custom_upstream_err  ', identityT)).toBe('custom_upstream_err')
      expect(getFailureReasonShortLabel('  custom_upstream_err  ', identityT)).toBe('custom_upstream_err')

      // Empty / null
      expect(getFailureReasonLabel(null, identityT)).toBe('admin.ops.realtimeRequests.status.failure')
      expect(getFailureReasonLabel('  ', identityT)).toBe('admin.ops.realtimeRequests.status.failure')
      expect(getFailureReasonShortLabel(null, identityT)).toBe('')
      expect(getFailureReasonShortLabel('   ', identityT)).toBe('')
    })
  })

  describe('isRequestSuccess', () => {
    it('respects explicit result semantics even on HTTP 200 and supports 101 success', () => {
      expect(
        isRequestSuccess({
          observed_at: '2026-11-24T00:00:00Z',
          request_id: 'req-200',
          result: 'success',
          http_status: 200
        })
      ).toBe(true)

      expect(
        isRequestSuccess({
          observed_at: '2026-11-24T00:00:00Z',
          request_id: 'req-101',
          result: 'success',
          http_status: 101
        })
      ).toBe(true)

      for (const result of ['upstream_error', 'rejected', 'client_abort', 'gateway_error', 'timeout']) {
        expect(
          isRequestSuccess({
            observed_at: '2026-11-24T00:00:00Z',
            request_id: `req-${result}`,
            result,
            http_status: 200
          })
        ).toBe(false)
      }
    })

    it('falls back to http_status and failure_reason when result is absent', () => {
      expect(
        isRequestSuccess({
          observed_at: '2026-11-24T00:00:00Z',
          request_id: 'req-fallback-ok',
          result: null,
          http_status: 200
        })
      ).toBe(true)

      expect(
        isRequestSuccess({
          observed_at: '2026-11-24T00:00:00Z',
          request_id: 'req-fallback-4xx',
          result: null,
          http_status: 401
        })
      ).toBe(false)

      expect(
        isRequestSuccess({
          observed_at: '2026-11-24T00:00:00Z',
          request_id: 'req-fallback-reason',
          result: null,
          http_status: null,
          failure_reason: 'client_auth_failed'
        })
      ).toBe(false)
    })
  })
})
