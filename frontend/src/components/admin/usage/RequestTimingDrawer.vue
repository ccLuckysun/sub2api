<template>
  <BaseDialog
    :show="show"
    :title="t('usage.timing.detailTitle')"
    width="wide"
    @close="handleClose"
  >
    <div class="space-y-4" data-testid="request-timing-drawer">
      <div v-if="loading" class="space-y-3">
        <div class="h-6 w-40 animate-pulse rounded bg-ui-surface-muted" />
        <div class="h-20 animate-pulse rounded-lg bg-ui-surface-muted" />
      </div>

      <div
        v-else-if="error"
        class="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-200"
      >
        <div class="font-medium">{{ t('usage.timing.loadFailed') }}</div>
        <div class="mt-1 text-xs">{{ error }}</div>
        <button type="button" class="btn btn-ghost btn-sm mt-2" @click="load">
          {{ t('common.retry') }}
        </button>
      </div>

      <template v-else>
        <dl class="grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="item in metaItems" :key="item.key" class="min-w-0">
            <dt class="text-ui-muted">{{ item.label }}</dt>
            <dd class="mt-0.5 flex items-center gap-1.5">
              <span
                class="truncate"
                :class="item.mono ? 'font-mono text-[11px] text-ui-ink' : 'text-ui-ink'"
                :title="item.value"
              >{{ item.value }}</span>
              <button
                v-if="item.copyable && item.value !== '—'"
                type="button"
                class="shrink-0 rounded p-0.5 text-ui-muted transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
                :title="copiedKey === item.key ? t('keys.copied') : t('keys.copyToClipboard')"
                @click="copy(item.key, item.value)"
              >
                <Icon :name="copiedKey === item.key ? 'check' : 'copy'" size="xs" />
              </button>
            </dd>
          </div>
        </dl>

        <div class="rounded-xl border border-ui p-4 dark:border-ui">
          <div class="mb-3 flex items-center justify-between gap-2">
            <h4 class="text-sm font-semibold text-ui-ink">{{ t('usage.timing.breakdownTitle') }}</h4>
            <HelpTooltip :text="t('usage.timing.breakdownHint')" />
          </div>
          <RequestTimingBreakdown :timing="timing" />
        </div>

        <div v-if="rawItems.length > 0" class="rounded-xl border border-ui p-4 dark:border-ui">
          <h4 class="mb-2 text-sm font-semibold text-ui-ink">{{ t('usage.timing.rawTitle') }}</h4>
          <dl class="grid gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2 lg:grid-cols-3">
            <div v-for="item in rawItems" :key="item.key" class="flex items-baseline justify-between gap-3">
              <dt class="text-ui-muted">{{ item.label }}</dt>
              <dd class="font-mono text-[11px] tabular-nums text-ui-ink">{{ item.value }}</dd>
            </div>
          </dl>
        </div>

        <p class="text-[11px] leading-relaxed text-ui-muted">
          {{ t('usage.timing.caveats') }}
        </p>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import RequestTimingBreakdown from './RequestTimingBreakdown.vue'
import { adminUsageAPI } from '@/api/admin/usage'
import type { AdminUsageLog } from '@/types'
import type { RequestTiming, RequestTimingDetail } from '@/types/requestTiming'
import { normalizeRequestTiming } from '@/types/requestTiming'
import {
  formatBodySize,
  formatBytesPerToken,
  formatBytesExact,
  formatTimingMs,
  formatUploadRate,
} from '@/utils/timingMetrics'

interface Props {
  show: boolean
  /** usage 列表行或 ops 慢请求行（后者已内嵌 timing，不再请求详情） */
  log?: AdminUsageLog | RequestTimingDetail | null
  /** ops 行自带完整详情；usage 行缺 timing 时才按需拉取一次 */
  source?: 'usage' | 'ops'
}

const props = withDefaults(defineProps<Props>(), {
  log: null,
  source: 'usage',
})

const emit = defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const detail = ref<RequestTimingDetail | null>(null)
const copiedKey = ref('')

// request_id 的客户端段前缀（后端 gateway 生成 `client:<uuid>`）。
const CLIENT_REQUEST_ID_PREFIX = 'client:'

// 乱序保护：每次加载递增序号，晚到的旧响应直接丢弃
let requestSeq = 0
let abortController: AbortController | null = null

const embeddedTiming = computed<RequestTiming | null>(() => {
  const log = props.log
  if (!log) return null
  const raw = (log as { timing?: unknown }).timing
  return normalizeRequestTiming(raw)
})

const timing = computed<RequestTiming | null>(() => {
  if (props.source === 'ops') return embeddedTiming.value
  return normalizeRequestTiming(detail.value?.timing) ?? embeddedTiming.value
})

/**
 * 客户端请求 id 的后端编码约定是 `client:<uuid>`（gateway 生成后回填到 request_id）。
 * 列表 DTO 里没有独立的 client_request_id 时，只有 request_id **明确以 `client:` 开头且非空**
 * 才派生后缀，保证可 grep；其余情况保持「—」，绝不推测 gateway/upstream 的 id。
 */
const deriveClientRequestID = (requestId: string | null | undefined): string | null => {
  const value = typeof requestId === 'string' ? requestId.trim() : ''
  if (!value.startsWith(CLIENT_REQUEST_ID_PREFIX)) return null
  const suffix = value.slice(CLIENT_REQUEST_ID_PREFIX.length).trim()
  return suffix ? suffix : null
}

const metaItems = computed(() => {
  const log = props.log as (AdminUsageLog & Partial<RequestTimingDetail>) | null
  // ops 慢请求行本身就是 RequestTimingDetail（内嵌四个 request id），不再发详情请求，
  // 因此这里必须回落到行的字段，否则 ops 侧这几个 id 会显示成破折号。
  const d = detail.value ?? (log as unknown as RequestTimingDetail | null)
  const stream = d?.stream ?? log?.stream
  return [
    { key: 'id', label: t('usage.timing.meta.id'), value: String(d?.id ?? log?.id ?? '—'), mono: true },
    {
      key: 'created_at',
      label: t('usage.timing.meta.createdAt'),
      value: (d?.created_at ?? log?.created_at) ? new Date(d?.created_at ?? log?.created_at ?? '').toLocaleString() : '—',
      mono: false,
    },
    { key: 'model', label: t('usage.timing.meta.model'), value: d?.model ?? log?.model ?? '—', mono: false },
    {
      key: 'stream',
      label: t('usage.timing.meta.stream'),
      value: typeof stream === 'boolean' ? t(stream ? 'common.yes' : 'common.no') : '—',
      mono: false,
    },
    {
      key: 'account_id',
      label: t('usage.timing.meta.accountId'),
      value: d?.account_id != null ? String(d.account_id) : (log?.account_id != null ? String(log.account_id) : '—'),
      mono: true,
    },
    {
      key: 'request_id',
      label: t('usage.timing.meta.requestId'),
      value: d?.request_id ?? log?.request_id ?? '—',
      mono: true,
      copyable: true,
    },
    {
      key: 'client_request_id',
      label: t('usage.timing.meta.clientRequestId'),
      value: d?.client_request_id ?? deriveClientRequestID(d?.request_id ?? log?.request_id) ?? '—',
      mono: true,
      copyable: true,
    },
    {
      key: 'gateway_request_id',
      label: t('usage.timing.meta.gatewayRequestId'),
      value: d?.gateway_request_id ?? '—',
      mono: true,
      copyable: true,
    },
    {
      key: 'upstream_request_id',
      label: t('usage.timing.meta.upstreamRequestId'),
      value: d?.upstream_request_id ?? (log as { upstream_request_id?: string | null } | null)?.upstream_request_id ?? '—',
      mono: true,
      copyable: true,
    },
  ]
})

const rawItems = computed(() => {
  const timingValue = timing.value
  if (!timingValue) return []
  return [
    { key: 'gateway', label: t('usage.timing.total'), value: formatTimingMs(timingValue.gateway_latency_ms) },
    { key: 'duration', label: t('usage.timing.duration'), value: formatTimingMs(timingValue.duration_ms) },
    { key: 'first_token', label: t('usage.timing.firstToken'), value: formatTimingMs(timingValue.first_token_ms) },
    { key: 'raw_upload', label: t('usage.timing.rawUpload'), value: formatTimingMs(timingValue.raw_upload_ms) },
    { key: 'residual', label: t('usage.timing.residualLabel'), value: formatTimingMs(timingValue.timing_residual_ms) },
    { key: 'body', label: t('usage.timing.bodyExact'), value: formatBytesExact(timingValue.body_bytes) },
    { key: 'body_mib', label: t('usage.timing.body'), value: formatBodySize(timingValue.body_bytes) },
    { key: 'rate', label: t('usage.timing.rate'), value: formatUploadRate(timingValue.upload_kbps) },
    {
      key: 'ctx_tokens',
      label: t('usage.timing.ctxTokens'),
      value: timingValue.ctx_tokens === null ? '—' : timingValue.ctx_tokens.toLocaleString(),
    },
    { key: 'bytes_per_token', label: t('usage.timing.bytesPerToken'), value: formatBytesPerToken(timingValue.bytes_per_token) },
    { key: 'method', label: t('usage.timing.method'), value: timingValue.timing_method ?? '—' },
  ]
})

const load = async () => {
  const log = props.log
  if (!log) return
  if (props.source === 'ops') return
  if (embeddedTiming.value) return

  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  const seq = ++requestSeq

  loading.value = true
  error.value = ''
  try {
    const result = await adminUsageAPI.getTiming(log.id, log.created_at, { signal: controller.signal })
    if (seq !== requestSeq) return // 乱序：已有更新请求
    detail.value = result
  } catch (err) {
    if (seq !== requestSeq) return
    if ((err as { name?: string })?.name === 'AbortError') return
    detail.value = null
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (seq === requestSeq) loading.value = false
  }
}

const handleClose = () => {
  abortController?.abort()
  abortController = null
  requestSeq += 1
  loading.value = false
  emit('close')
}

const copy = async (key: string, value: string) => {
  try {
    await navigator.clipboard.writeText(value)
    copiedKey.value = key
    setTimeout(() => {
      if (copiedKey.value === key) copiedKey.value = ''
    }, 1500)
  } catch {
    copiedKey.value = ''
  }
}

watch(
  () => [props.show, props.log?.id] as const,
  ([show]) => {
    if (!show) {
      abortController?.abort()
      abortController = null
      requestSeq += 1
      loading.value = false
      return
    }
    detail.value = null
    error.value = ''
    copiedKey.value = ''
    void load()
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  abortController?.abort()
  abortController = null
})
</script>
