<template>
  <div class="space-y-3" data-testid="request-timing-breakdown">
    <div class="flex flex-wrap items-center gap-2">
      <span
        class="inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-semibold ring-1 ring-inset"
        :class="statusMeta.badgeClass"
        data-testid="timing-status"
      >
        {{ t(statusMeta.labelKey) }}
      </span>
      <span
        v-if="timing?.timing_method"
        class="rounded bg-ui-surface-muted px-1.5 py-0.5 font-mono text-[10px] text-ui-muted"
      >{{ timing.timing_method }}</span>
      <span v-if="residualHint" class="text-[11px] text-amber-600 dark:text-amber-400">
        {{ residualHint }}
      </span>
    </div>

    <div v-if="loading" class="h-16 animate-pulse rounded-lg bg-ui-surface-muted" />

    <template v-else-if="segments">
      <!-- 三段比例条：按未舍入值占比计算宽度，短段不放大、不改比例 -->
      <div
        class="flex h-3 w-full overflow-hidden rounded-full bg-ui-surface-muted"
        role="img"
        :aria-label="barAriaLabel"
      >
        <div
          v-for="seg in barSegments"
          :key="seg.key"
          :class="seg.class"
          :style="{ width: seg.percent }"
          :title="`${seg.label} ${seg.valueText}`"
        />
      </div>

      <!-- 外置标签：窄屏换行而不是压缩比例 -->
      <div class="flex flex-wrap items-start gap-x-6 gap-y-2 text-xs">
        <div v-for="seg in barSegments" :key="seg.key" class="min-w-[120px]">
          <div class="flex items-center gap-1.5 text-ui-muted">
            <span class="h-2 w-2 shrink-0 rounded-full" :class="seg.class" aria-hidden="true" />
            <span>{{ seg.label }}</span>
          </div>
          <div class="mt-0.5 font-medium tabular-nums text-ui-ink">{{ seg.valueText }}</div>
        </div>
        <div class="min-w-[120px]">
          <div class="text-ui-muted">{{ t('usage.timing.total') }}</div>
          <div class="mt-0.5 font-medium tabular-nums text-ui-ink" data-testid="timing-total">
            {{ formatTimingSeconds(timing?.gateway_latency_ms) }}
          </div>
        </div>
        <div class="min-w-[140px]">
          <div class="flex items-center gap-1 text-ui-muted">
            <span>{{ t('usage.timing.realTtfb') }}</span>
            <HelpTooltip :text="t('usage.timing.realTtfbHint')" />
          </div>
          <div class="mt-0.5 font-medium tabular-nums text-ui-ink" data-testid="timing-real-ttfb">
            {{ formatTimingSeconds(timing?.real_ttfb_ms) }}
          </div>
        </div>
      </div>

      <div v-if="!compact" class="grid gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2 lg:grid-cols-3">
        <div v-for="item in detailItems" :key="item.key" class="flex items-baseline justify-between gap-3 border-b border-ui/60 pb-1 dark:border-ui/60">
          <span class="text-ui-muted">{{ item.label }}</span>
          <span class="font-medium tabular-nums text-ui-ink">{{ item.value }}</span>
        </div>
      </div>
    </template>

    <div v-else class="rounded-lg border border-dashed border-ui px-3 py-3 text-xs text-ui-muted dark:border-ui">
      <div class="font-medium text-ui-ink">{{ t('usage.timing.unavailableTitle') }}</div>
      <div class="mt-1">{{ t('usage.timing.unavailableHint') }}</div>
      <dl v-if="fallbackItems.length > 0" class="mt-2 grid gap-1 sm:grid-cols-2">
        <div v-for="item in fallbackItems" :key="item.key" class="flex items-baseline justify-between gap-3">
          <dt class="text-ui-muted">{{ item.label }}</dt>
          <dd class="font-medium tabular-nums text-ui-ink">{{ item.value }}</dd>
        </div>
      </dl>
    </div>

    <ul v-if="reasonTexts.length > 0" class="list-inside list-disc text-[11px] text-amber-600 dark:text-amber-400">
      <li v-for="reasonText in reasonTexts" :key="reasonText">{{ reasonText }}</li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RequestTiming } from '@/types/requestTiming'
import { timingSegments } from '@/types/requestTiming'
import {
  TIMING_SEGMENT_CLASSES,
  TIMING_STATUS_META,
  formatBodySize,
  formatBytesPerToken,
  formatBytesExact,
  formatTimingMs,
  formatTimingSeconds,
  formatTokensPerSecond,
  formatUploadRate,
  timingReasonLabelKey,
  uploadSeverity,
} from '@/utils/timingMetrics'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

interface Props {
  timing: RequestTiming | null
  loading?: boolean
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  compact: false,
})

const { t } = useI18n()

const statusMeta = computed(() => TIMING_STATUS_META[props.timing?.timing_status ?? 'unavailable'])

const segments = computed(() => timingSegments(props.timing))

const barSegments = computed(() => {
  const seg = segments.value
  if (!seg) return []
  const uploadClass = uploadSeverity(seg.upload) === 'critical'
    ? TIMING_SEGMENT_CLASSES.uploadCritical
    : TIMING_SEGMENT_CLASSES.upload
  return [
    {
      key: 'upload',
      label: t('usage.timing.uploadEstimated'),
      valueText: formatTimingSeconds(seg.upload),
      class: uploadClass,
      percent: `${(seg.upload / seg.total) * 100}%`,
    },
    {
      key: 'upstream',
      label: t('usage.timing.upstreamTtft'),
      valueText: formatTimingSeconds(seg.upstreamTtft),
      class: TIMING_SEGMENT_CLASSES.upstreamTtft,
      percent: `${(seg.upstreamTtft / seg.total) * 100}%`,
    },
    {
      key: 'stream',
      label: t('usage.timing.stream'),
      valueText: formatTimingSeconds(seg.stream),
      class: TIMING_SEGMENT_CLASSES.stream,
      percent: `${(seg.stream / seg.total) * 100}%`,
    },
  ]
})

const barAriaLabel = computed(() => {
  const seg = segments.value
  if (!seg) return t('usage.timing.unavailableTitle')
  return t('usage.timing.barAriaLabel', {
    upload: formatTimingSeconds(seg.upload),
    upstream: formatTimingSeconds(seg.upstreamTtft),
    stream: formatTimingSeconds(seg.stream),
    total: formatTimingSeconds(props.timing?.gateway_latency_ms ?? seg.total),
  })
})

const residualHint = computed(() => {
  const timing = props.timing
  if (!timing || timing.timing_residual_ms === null) return ''
  if (Math.abs(timing.timing_residual_ms) < 1) return ''
  return t('usage.timing.residual', { value: formatTimingMs(Math.abs(timing.timing_residual_ms)) })
})

const detailItems = computed(() => {
  const timing = props.timing
  if (!timing) return []
  return [
    { key: 'duration', label: t('usage.timing.duration'), value: formatTimingSeconds(timing.duration_ms) },
    { key: 'first_token', label: t('usage.timing.firstToken'), value: formatTimingMs(timing.first_token_ms) },
    { key: 'body', label: t('usage.timing.body'), value: formatBodySize(timing.body_bytes) },
    { key: 'body_bytes', label: t('usage.timing.bodyExact'), value: formatBytesExact(timing.body_bytes) },
    { key: 'rate', label: t('usage.timing.rate'), value: formatUploadRate(timing.upload_kbps) },
    { key: 'ctx_tokens', label: t('usage.timing.ctxTokens'), value: timing.ctx_tokens === null ? '—' : timing.ctx_tokens.toLocaleString() },
    { key: 'bytes_per_token', label: t('usage.timing.bytesPerToken'), value: formatBytesPerToken(timing.bytes_per_token) },
    { key: 'tps_total', label: t('usage.timing.tpsTotal'), value: formatTokensPerSecond(timing.tps_total) },
    { key: 'tps_stream', label: t('usage.timing.tpsStream'), value: formatTokensPerSecond(timing.tps_stream) },
  ]
})

const fallbackItems = computed(() => {
  const timing = props.timing
  if (!timing) return []
  const items: { key: string; label: string; value: string }[] = []
  if (timing.gateway_latency_ms !== null) {
    items.push({ key: 'gateway', label: t('usage.timing.total'), value: formatTimingSeconds(timing.gateway_latency_ms) })
  }
  if (timing.duration_ms !== null) {
    items.push({ key: 'duration', label: t('usage.timing.duration'), value: formatTimingSeconds(timing.duration_ms) })
  }
  if (timing.first_token_ms !== null) {
    items.push({ key: 'first_token', label: t('usage.timing.firstToken'), value: formatTimingMs(timing.first_token_ms) })
  }
  if (timing.body_bytes !== null) {
    items.push({ key: 'body', label: t('usage.timing.body'), value: formatBodySize(timing.body_bytes) })
  }
  return items
})

const reasonTexts = computed(() => {
  const reason = props.timing?.reason
  if (!reason) return []
  const key = timingReasonLabelKey(reason)
  return [key ? t(key) : reason]
})
</script>
