<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import { useAppStore } from '@/stores'
import type { OpsRealtimeRequestItem } from '@/api/admin/ops'
import {
  computePhaseBreakdown,
  formatByteSize,
  formatDurationMs,
  formatObservedDateTimeFull,
  getByteStatusBadge,
  getFailureReasonLabel,
  getHttpStatusClass,
  isRequestSuccess
} from '../utils/realtimeRequestFormatters'

interface Props {
  show: boolean
  item: OpsRealtimeRequestItem | null
}

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()

const close = () => emit('update:show', false)

const dialogTitle = computed(() => {
  if (!props.item?.request_id) return t('admin.ops.realtimeRequests.detail.title')
  const shortId =
    props.item.request_id.length > 24
      ? `${props.item.request_id.slice(0, 24)}…`
      : props.item.request_id
  return `${t('admin.ops.realtimeRequests.detail.title')} · ${shortId}`
})

const phaseBreakdown = computed(() => {
  if (!props.item) return null
  return computePhaseBreakdown(props.item)
})

const requestSucceeded = computed(() => {
  if (!props.item) return true
  return isRequestSuccess(props.item)
})

const failureReasonDisplay = computed(() => {
  if (!props.item) return '—'
  if (!props.item.failure_reason) return requestSucceeded.value ? '—' : t('admin.ops.realtimeRequests.status.failure')
  return getFailureReasonLabel(props.item.failure_reason, t)
})

const rawFailureReason = computed(() => String(props.item?.failure_reason ?? '').trim())

const hasUpstreamStatus = computed(() => {
  const status = props.item?.upstream_status
  return typeof status === 'number' && Number.isFinite(status) && status > 0
})

const upstreamErrorCode = computed(() => String(props.item?.upstream_error_code ?? '').trim())

const isUpstreamErrorCode = computed(() => {
  if (hasUpstreamStatus.value) return true
  const attempts = props.item?.attempt_count
  return typeof attempts === 'number' && Number.isFinite(attempts) && attempts > 0
})

const normalizedErrorMessage = computed(() => String(props.item?.error_message ?? '').trim())
const hasErrorMessage = computed(() => normalizedErrorMessage.value.length > 0)

const retryCount = computed(() => {
  const attempts = props.item?.attempt_count
  if (typeof attempts !== 'number' || !Number.isFinite(attempts)) return 0
  return Math.max(0, attempts - 1)
})

const httpMethod = computed(() => String(props.item?.http_method ?? '').trim().toUpperCase())

const byteFlowRows = computed(() => {
  const row = props.item
  if (!row) return []
  return [
    {
      key: 'inWire',
      label: t('admin.ops.realtimeRequests.detail.directions.inWire'),
      field: 'bytes_in_wire',
      bytes: row.bytes_in_wire,
      status: row.bytes_in_wire_status,
      extraHint:
        typeof row.bytes_in_declared === 'number' && row.bytes_in_declared >= 0
          ? t('admin.ops.realtimeRequests.detail.bytesDeclaredHint', {
              size: formatByteSize(row.bytes_in_declared)
            })
          : ''
    },
    {
      key: 'inDecoded',
      label: t('admin.ops.realtimeRequests.detail.directions.inDecoded'),
      field: 'bytes_in_decoded',
      bytes: row.bytes_in_decoded,
      status:
        typeof row.bytes_in_decoded === 'number' && row.bytes_in_decoded >= 0
          ? row.bytes_in_wire_status || 'complete'
          : 'unknown',
      extraHint: ''
    },
    {
      key: 'upReqTotal',
      label: t('admin.ops.realtimeRequests.detail.directions.upReqTotal'),
      field: 'bytes_up_req_total',
      bytes: row.bytes_up_req_total,
      status: row.bytes_up_req_total_status,
      extraHint: ''
    },
    {
      key: 'respUpstream',
      label: t('admin.ops.realtimeRequests.detail.directions.respUpstream'),
      field: 'bytes_resp_upstream',
      bytes: row.bytes_resp_upstream,
      status: row.bytes_resp_upstream_status,
      extraHint: ''
    },
    {
      key: 'respDownstream',
      label: t('admin.ops.realtimeRequests.detail.directions.respDownstream'),
      field: 'bytes_resp_downstream',
      bytes: row.bytes_resp_downstream,
      status: row.bytes_resp_downstream_status,
      extraHint: ''
    }
  ]
})

async function copyText(val?: string | null) {
  const trimmed = val?.trim()
  if (!trimmed) return
  const ok = await copyToClipboard(trimmed, t('admin.ops.realtimeRequests.detail.copied'))
  if (!ok) {
    appStore.showWarning(t('admin.ops.realtimeRequests.detail.copyFailed'))
  }
}

function formatRawBytes(bytes?: number | null): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return ''
  return `${Math.round(bytes).toLocaleString()} B`
}
</script>

<template>
  <BaseDialog
    :show="show"
    :title="dialogTitle"
    width="extra-wide"
    :close-on-click-outside="true"
    @close="close"
  >
    <div v-if="!item" class="py-10 text-center text-sm text-ui-muted">
      {{ t('common.noData') }}
    </div>

    <div v-else class="space-y-5">
      <!-- Top 4 KPI Tiles -->
      <div data-testid="detail-kpi-grid" class="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <div class="rounded-xl border border-ui bg-gray-50 p-3.5 dark:bg-ui-canvas">
          <div class="text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.kpi.totalDuration') }}
          </div>
          <div
            class="mt-1 font-mono text-lg font-bold"
            :class="
              (item.total_ms ?? 0) >= 3000
                ? 'text-amber-600 dark:text-amber-400'
                : 'text-ui-ink'
            "
          >
            {{ formatDurationMs(item.total_ms) }}
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50 p-3.5 dark:bg-ui-canvas">
          <div class="text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.kpi.ttft') }}
          </div>
          <div class="mt-1 font-mono text-lg font-bold text-ui-ink">
            {{ formatDurationMs(item.headers_end_ms) }}
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50 p-3.5 dark:bg-ui-canvas">
          <div class="text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.kpi.requestBody') }}
          </div>
          <div class="mt-1 font-mono text-lg font-bold text-ui-ink">
            {{ formatByteSize(item.bytes_in_wire) }}
          </div>
        </div>

        <div class="rounded-xl border border-ui bg-gray-50 p-3.5 dark:bg-ui-canvas">
          <div class="text-[11px] font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.kpi.httpStatus') }}
          </div>
          <div class="mt-1.5 flex items-center gap-2">
            <span
              class="inline-flex items-center rounded-lg px-2.5 py-0.5 font-mono text-sm font-bold ring-1 ring-inset"
              :class="getHttpStatusClass(item.http_status)"
            >
              {{ item.http_status ?? '—' }}
            </span>
            <span
              class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-semibold"
              :class="
                requestSucceeded
                  ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
                  : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
              "
            >
              {{
                requestSucceeded
                  ? t('admin.ops.realtimeRequests.status.success')
                  : t('admin.ops.realtimeRequests.status.failure')
              }}
            </span>
          </div>
        </div>
      </div>

      <!-- Failure Diagnostic Banner (First-Screen Visibility) -->
      <section
        v-if="!requestSucceeded"
        data-testid="failure-diagnostic-banner"
        class="rounded-xl border border-red-200 bg-red-50/80 p-4 dark:border-red-800/50 dark:bg-red-900/20"
      >
        <!-- Header: Title + Failure Category Label + Raw Enum -->
        <div class="flex flex-wrap items-start justify-between gap-2">
          <div class="flex flex-wrap items-center gap-2">
            <Icon
              name="exclamationTriangle"
              size="md"
              class="shrink-0 text-red-600 dark:text-red-400"
            />
            <h4 class="text-sm font-semibold text-red-900 dark:text-red-200">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.title') }}
            </h4>
            <span
              class="inline-flex items-center rounded-full bg-red-100 px-2.5 py-0.5 text-xs font-semibold text-red-800 dark:bg-red-900/40 dark:text-red-200"
            >
              {{ failureReasonDisplay }}
            </span>
            <code
              v-if="rawFailureReason"
              data-testid="failure-raw-reason"
              class="rounded bg-red-100/80 px-1.5 py-0.5 font-mono text-[11px] font-medium text-red-700 ring-1 ring-inset ring-red-200 dark:bg-red-950/60 dark:text-red-300 dark:ring-red-800/60"
            >
              {{ rawFailureReason }}
            </code>
          </div>
        </div>

        <!-- Diagnostic Metadata Strip: Upstream Status/Code + Retry & Attempt Counts -->
        <div class="mt-3 flex flex-wrap items-center gap-2 text-xs">
          <div
            v-if="hasUpstreamStatus"
            data-testid="failure-upstream-status"
            class="inline-flex items-center gap-1.5 rounded-lg bg-white/80 px-2.5 py-1 ring-1 ring-inset ring-red-200/80 dark:bg-dark-800/70 dark:ring-red-800/50"
          >
            <span class="text-red-700/80 dark:text-red-300/80">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.upstreamStatus') }}
            </span>
            <span
              class="inline-flex items-center rounded px-1.5 py-0.5 font-mono text-[11px] font-semibold ring-1 ring-inset"
              :class="getHttpStatusClass(item.upstream_status)"
            >
              {{ item.upstream_status }}
            </span>
          </div>

          <div
            v-if="upstreamErrorCode"
            data-testid="failure-upstream-error-code"
            class="inline-flex items-center gap-1.5 rounded-lg bg-white/80 px-2.5 py-1 ring-1 ring-inset ring-red-200/80 dark:bg-dark-800/70 dark:ring-red-800/50"
          >
            <span class="text-red-700/80 dark:text-red-300/80">
              {{
                isUpstreamErrorCode
                  ? t('admin.ops.realtimeRequests.detail.failureBanner.upstreamErrorCode')
                  : t('admin.ops.realtimeRequests.detail.failureBanner.errorCode')
              }}
            </span>
            <code class="font-mono text-[11px] font-semibold text-red-950 dark:text-red-100">
              {{ upstreamErrorCode }}
            </code>
          </div>

          <div
            data-testid="failure-retry-count"
            class="inline-flex items-center gap-1.5 rounded-lg bg-white/80 px-2.5 py-1 ring-1 ring-inset ring-red-200/80 dark:bg-dark-800/70 dark:ring-red-800/50"
          >
            <span class="text-red-700/80 dark:text-red-300/80">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.retryCount') }}
            </span>
            <span
              class="font-mono font-semibold"
              :class="retryCount > 0 ? 'text-amber-700 dark:text-amber-300' : 'text-red-950 dark:text-red-100'"
            >
              {{ retryCount }}
            </span>
          </div>

          <div
            data-testid="failure-attempt-count"
            class="inline-flex items-center gap-1.5 rounded-lg bg-white/80 px-2.5 py-1 ring-1 ring-inset ring-red-200/80 dark:bg-dark-800/70 dark:ring-red-800/50"
          >
            <span class="text-red-700/80 dark:text-red-300/80">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.attemptCount') }}
            </span>
            <span class="font-mono font-semibold text-red-950 dark:text-red-100">
              {{ item.attempt_count ?? '—' }}
            </span>
          </div>

          <div
            data-testid="failure-failed-attempts"
            class="inline-flex items-center gap-1.5 rounded-lg bg-white/80 px-2.5 py-1 ring-1 ring-inset ring-red-200/80 dark:bg-dark-800/70 dark:ring-red-800/50"
          >
            <span class="text-red-700/80 dark:text-red-300/80">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.failedAttempts') }}
            </span>
            <span
              class="font-mono font-semibold"
              :class="(item.failed_attempts ?? 0) > 0 ? 'text-amber-700 dark:text-amber-300' : 'text-red-950 dark:text-red-100'"
            >
              {{ item.failed_attempts ?? '—' }}
            </span>
          </div>
        </div>

        <!-- Error Message Box with One-Click Copy -->
        <div
          class="mt-3 rounded-lg border border-red-200/80 bg-white/90 p-3 dark:border-red-800/50 dark:bg-dark-900/60"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="text-[11px] font-semibold uppercase tracking-wider text-red-700 dark:text-red-300">
              {{ t('admin.ops.realtimeRequests.detail.failureBanner.errorMessage') }}
            </span>
            <button
              v-if="hasErrorMessage"
              type="button"
              data-testid="copy-error-message-btn"
              class="inline-flex shrink-0 items-center gap-1 rounded-md bg-red-100/80 px-2 py-1 text-[10px] font-semibold text-red-800 transition-colors hover:bg-red-200/80 dark:bg-red-900/40 dark:text-red-200 dark:hover:bg-red-900/60"
              @click="copyText(normalizedErrorMessage)"
            >
              <Icon name="copy" size="xs" />
              {{ t('admin.ops.realtimeRequests.detail.copy') }}
            </button>
          </div>
          <pre
            v-if="hasErrorMessage"
            data-testid="failure-error-message"
            class="mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-red-950 dark:text-red-100"
          >{{ normalizedErrorMessage }}</pre>
          <div
            v-else
            data-testid="failure-error-message-empty"
            class="mt-1.5 text-xs italic text-red-700/70 dark:text-red-300/70"
          >
            {{ t('admin.ops.realtimeRequests.detail.failureBanner.emptyErrorMessage') }}
          </div>
        </div>
      </section>

      <!-- Basic Info Grid -->
      <section class="rounded-xl border border-ui bg-gray-50/70 p-4 dark:bg-ui-canvas">
        <h4 class="mb-3 text-xs font-semibold uppercase tracking-wider text-ui-muted">
          {{ t('admin.ops.realtimeRequests.detail.basicInfo') }}
        </h4>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <!-- Request ID -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.requestId') }}
            </div>
            <div class="mt-1 flex items-center gap-1.5">
              <span class="min-w-0 flex-1 break-all font-mono text-xs font-medium text-ui-ink">
                {{ item.request_id || '—' }}
              </span>
              <button
                v-if="item.request_id"
                type="button"
                class="inline-flex shrink-0 items-center gap-1 rounded-md bg-gray-100 px-2 py-1 text-[10px] font-semibold text-ui-muted transition-colors hover:bg-gray-200 hover:text-ui-ink dark:bg-dark-700 dark:text-ui-ink dark:hover:bg-dark-600"
                @click="copyText(item.request_id)"
              >
                <Icon name="copy" size="xs" />
                {{ t('admin.ops.realtimeRequests.detail.copy') }}
              </button>
            </div>
          </div>

          <!-- Client Request ID -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.clientRequestId') }}
            </div>
            <div class="mt-1 flex items-center gap-1.5">
              <span class="min-w-0 flex-1 break-all font-mono text-xs font-medium text-ui-ink">
                {{ item.client_request_id || '—' }}
              </span>
              <button
                v-if="item.client_request_id"
                type="button"
                class="inline-flex shrink-0 items-center gap-1 rounded-md bg-gray-100 px-2 py-1 text-[10px] font-semibold text-ui-muted transition-colors hover:bg-gray-200 hover:text-ui-ink dark:bg-dark-700 dark:text-ui-ink dark:hover:bg-dark-600"
                @click="copyText(item.client_request_id)"
              >
                <Icon name="copy" size="xs" />
                {{ t('admin.ops.realtimeRequests.detail.copy') }}
              </button>
            </div>
          </div>

          <!-- Observed At -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.observedAt') }}
            </div>
            <div class="mt-1 font-mono text-xs font-medium text-ui-ink" :title="item.observed_at">
              {{ formatObservedDateTimeFull(item.observed_at) }}
            </div>
          </div>

          <!-- Node -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.node') }}
            </div>
            <div class="mt-1 break-all font-mono text-xs font-medium text-ui-ink">
              {{ item.node || '—' }}
            </div>
          </div>

          <!-- User -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.user') }}
            </div>
            <div class="mt-1 break-all text-xs font-medium text-ui-ink">
              {{ item.user_email || (item.user_id != null ? `#${item.user_id}` : '—') }}
              <span v-if="item.user_email && item.user_id != null" class="ml-1 font-mono text-[11px] text-ui-muted">
                (#{{ item.user_id }})
              </span>
            </div>
          </div>

          <!-- API Key -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.apiKey') }}
            </div>
            <div class="mt-1 break-all text-xs font-medium text-ui-ink">
              <span>{{ item.api_key_name || (item.api_key_id != null ? `#${item.api_key_id}` : '—') }}</span>
              <span v-if="item.api_key_masked" class="ml-1.5 font-mono text-[11px] text-ui-muted">
                {{ item.api_key_masked }}
              </span>
            </div>
          </div>

          <!-- Account -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.account') }}
            </div>
            <div class="mt-1 break-all text-xs font-medium text-ui-ink">
              {{ item.account_name || (item.account_id != null ? `#${item.account_id}` : '—') }}
              <span v-if="item.account_name && item.account_id != null" class="ml-1 font-mono text-[11px] text-ui-muted">
                (#{{ item.account_id }})
              </span>
            </div>
          </div>

          <!-- Group -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.group') }}
            </div>
            <div class="mt-1 break-all text-xs font-medium text-ui-ink">
              {{ item.group_name || (item.group_id != null ? `#${item.group_id}` : '—') }}
              <span v-if="item.group_name && item.group_id != null" class="ml-1 font-mono text-[11px] text-ui-muted">
                (#{{ item.group_id }})
              </span>
            </div>
          </div>

          <!-- Platform -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.platform') }}
            </div>
            <div class="mt-1 text-xs font-semibold uppercase text-ui-ink">
              {{ item.platform || '—' }}
            </div>
          </div>

          <!-- Model -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.model') }}
            </div>
            <div class="mt-1 break-all font-mono text-xs font-medium text-ui-ink">
              {{ item.model || '—' }}
            </div>
          </div>

          <!-- Endpoint -->
          <div class="rounded-lg bg-ui-surface p-2.5 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700 sm:col-span-2">
            <div class="text-[11px] text-ui-muted">
              {{ t('admin.ops.realtimeRequests.detail.fields.endpoint') }}
            </div>
            <div class="mt-1 flex flex-wrap items-center gap-2 font-mono text-xs font-medium text-ui-ink">
              <span
                v-if="httpMethod"
                data-testid="detail-http-method"
                class="rounded bg-primary-50 px-1.5 py-0.5 text-[10px] font-bold uppercase text-primary-700 ring-1 ring-inset ring-primary-200 dark:bg-primary-900/30 dark:text-primary-300 dark:ring-primary-800/50"
              >
                {{ httpMethod }}
              </span>
              <span class="break-all">{{ item.endpoint || '—' }}</span>
              <span
                v-if="item.endpoint_class"
                class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-semibold text-ui-muted dark:bg-dark-700 dark:text-ui-ink"
              >
                {{ item.endpoint_class }}
              </span>
            </div>
          </div>
        </div>
      </section>

      <!-- 6-Phase Latency Breakdown -->
      <section v-if="phaseBreakdown" class="rounded-xl border border-ui bg-gray-50/70 p-4 dark:bg-ui-canvas">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h4 class="text-xs font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.phaseBreakdown') }}
          </h4>
          <span class="text-[11px] text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.ttftNote') }}
          </span>
        </div>

        <!-- Stacked Overview Bar -->
        <div class="mb-4 flex h-3 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
          <div
            v-for="seg in phaseBreakdown.segments"
            :key="seg.key"
            v-show="seg.percent > 0"
            class="h-full transition-all"
            :style="{ width: `${seg.percent}%`, backgroundColor: seg.color }"
            :title="`${t(seg.labelKey)}: ${formatDurationMs(seg.valueMs)} (${seg.percent.toFixed(1)}%)`"
          />
        </div>

        <div class="overflow-hidden rounded-lg border border-ui bg-ui-surface">
          <table class="min-w-full divide-y divide-gray-200 text-xs dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800/70">
              <tr>
                <th class="px-3 py-2 text-left font-semibold text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.detail.phaseCol') }}
                </th>
                <th class="px-3 py-2 text-right font-semibold text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.detail.durationCol') }}
                </th>
                <th class="w-48 px-3 py-2 text-left font-semibold text-ui-muted sm:w-64">
                  {{ t('admin.ops.realtimeRequests.detail.shareCol') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
              <tr v-for="seg in phaseBreakdown.segments" :key="seg.key">
                <td class="whitespace-nowrap px-3 py-2">
                  <div class="flex items-center gap-2">
                    <span
                      class="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                      :style="{ backgroundColor: seg.color }"
                    />
                    <span class="font-medium text-ui-ink">{{ t(seg.labelKey) }}</span>
                    <span class="hidden font-mono text-[10px] text-ui-muted sm:inline">{{ seg.field }}</span>
                  </div>
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-right font-mono font-medium text-ui-ink">
                  {{ formatDurationMs(seg.valueMs) }}
                </td>
                <td class="px-3 py-2">
                  <div class="flex items-center gap-2">
                    <div class="h-2 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                      <div
                        class="h-full rounded-full"
                        :style="{ width: `${Math.min(100, seg.percent)}%`, backgroundColor: seg.color }"
                      />
                    </div>
                    <span class="w-12 text-right font-mono text-[11px] text-ui-muted">
                      {{ seg.valueMs != null ? `${seg.percent.toFixed(1)}%` : '—' }}
                    </span>
                  </div>
                </td>
              </tr>
              <!-- Sum row -->
              <tr class="bg-gray-50/80 font-semibold dark:bg-dark-800/50">
                <td class="whitespace-nowrap px-3 py-2 text-ui-ink">
                  {{ t('admin.ops.realtimeRequests.detail.phaseSum') }}
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-right font-mono text-ui-ink">
                  {{ phaseBreakdown.hasAnyPhase ? formatDurationMs(phaseBreakdown.sumMs) : '—' }}
                </td>
                <td class="px-3 py-2 text-right font-mono text-[11px] text-ui-muted">
                  {{ phaseBreakdown.sumMs > 0 ? '100.0%' : '—' }}
                </td>
              </tr>
              <!-- TTFT overlay reference row -->
              <tr class="bg-amber-50/40 dark:bg-amber-900/10">
                <td class="whitespace-nowrap px-3 py-2">
                  <div class="flex items-center gap-2">
                    <span class="inline-block h-2.5 w-2.5 shrink-0 rounded-full border-2 border-amber-500" />
                    <span class="font-medium text-ui-ink">{{ t('admin.ops.realtimeRequests.phases.ttft') }}</span>
                    <span class="hidden font-mono text-[10px] text-ui-muted sm:inline">headers_end_ms</span>
                  </div>
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-right font-mono font-medium text-ui-ink">
                  {{ formatDurationMs(item.headers_end_ms) }}
                </td>
                <td class="px-3 py-2 text-[11px] text-ui-muted">
                  {{ t('admin.ops.realtimeRequests.phases.legendNote') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- 5-Direction Byte Flow + Failure/Retry -->
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <!-- 5-Direction Byte Flow -->
        <section class="rounded-xl border border-ui bg-gray-50/70 p-4 dark:bg-ui-canvas lg:col-span-2">
          <h4 class="mb-3 text-xs font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.byteFlow') }}
          </h4>
          <div class="overflow-hidden rounded-lg border border-ui bg-ui-surface">
            <table class="min-w-full divide-y divide-gray-200 text-xs dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800/70">
                <tr>
                  <th class="px-3 py-2 text-left font-semibold text-ui-muted">
                    {{ t('admin.ops.realtimeRequests.detail.directionCol') }}
                  </th>
                  <th class="px-3 py-2 text-right font-semibold text-ui-muted">
                    {{ t('admin.ops.realtimeRequests.detail.bytesCol') }}
                  </th>
                  <th class="px-3 py-2 text-right font-semibold text-ui-muted">
                    {{ t('admin.ops.realtimeRequests.detail.statusCol') }}
                  </th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                <tr v-for="row in byteFlowRows" :key="row.key">
                  <td class="px-3 py-2">
                    <div class="font-medium text-ui-ink">{{ row.label }}</div>
                    <div class="font-mono text-[10px] text-ui-muted">
                      {{ row.field }}
                      <span v-if="row.extraHint" class="ml-1.5 font-sans">· {{ row.extraHint }}</span>
                    </div>
                  </td>
                  <td class="whitespace-nowrap px-3 py-2 text-right font-mono">
                    <div class="font-medium text-ui-ink">{{ formatByteSize(row.bytes) }}</div>
                    <div v-if="formatRawBytes(row.bytes)" class="text-[10px] text-ui-muted">
                      {{ formatRawBytes(row.bytes) }}
                    </div>
                  </td>
                  <td class="whitespace-nowrap px-3 py-2 text-right">
                    <span
                      class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold"
                      :class="getByteStatusBadge(row.status, t).badgeClass"
                    >
                      {{ getByteStatusBadge(row.status, t).label }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- Failure & Retry Info -->
        <section class="rounded-xl border border-ui bg-gray-50/70 p-4 dark:bg-ui-canvas">
          <h4 class="mb-3 text-xs font-semibold uppercase tracking-wider text-ui-muted">
            {{ t('admin.ops.realtimeRequests.detail.failureRetry') }}
          </h4>
          <div class="space-y-2.5 text-xs">
            <div class="flex items-center justify-between rounded-lg bg-ui-surface px-3 py-2 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
              <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.detail.resultLabel') }}</span>
              <span
                class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-semibold"
                :class="
                  requestSucceeded
                    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
                    : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
                "
              >
                {{
                  requestSucceeded
                    ? t('admin.ops.realtimeRequests.status.success')
                    : t('admin.ops.realtimeRequests.status.failure')
                }}
              </span>
            </div>

            <div class="rounded-lg bg-ui-surface px-3 py-2 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
              <div class="flex items-center justify-between gap-2">
                <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.detail.failureReasonLabel') }}</span>
                <span
                  class="font-medium"
                  :class="item.failure_reason ? 'text-red-600 dark:text-red-400' : 'text-ui-ink'"
                >
                  {{ failureReasonDisplay }}
                </span>
              </div>
              <div
                v-if="item.failure_reason && failureReasonDisplay !== item.failure_reason"
                class="mt-1 text-right font-mono text-[10px] text-ui-muted"
              >
                {{ item.failure_reason }}
              </div>
            </div>

            <div class="flex items-center justify-between rounded-lg bg-ui-surface px-3 py-2 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
              <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.detail.httpStatusLabel') }}</span>
              <span
                class="inline-flex items-center rounded-md px-2 py-0.5 font-mono text-xs font-semibold ring-1 ring-inset"
                :class="getHttpStatusClass(item.http_status)"
              >
                {{ item.http_status ?? '—' }}
              </span>
            </div>

            <div class="flex items-center justify-between rounded-lg bg-ui-surface px-3 py-2 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
              <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.detail.attemptCountLabel') }}</span>
              <span class="font-mono font-semibold text-ui-ink">
                {{ item.attempt_count ?? '—' }}
              </span>
            </div>

            <div class="flex items-center justify-between rounded-lg bg-ui-surface px-3 py-2 ring-1 ring-inset ring-gray-200/70 dark:ring-dark-700">
              <span class="text-ui-muted">{{ t('admin.ops.realtimeRequests.detail.failedAttemptsLabel') }}</span>
              <span
                class="font-mono font-semibold"
                :class="(item.failed_attempts ?? 0) > 0 ? 'text-amber-600 dark:text-amber-400' : 'text-ui-ink'"
              >
                {{ item.failed_attempts ?? '—' }}
              </span>
            </div>
          </div>
        </section>
      </div>
    </div>
  </BaseDialog>
</template>
