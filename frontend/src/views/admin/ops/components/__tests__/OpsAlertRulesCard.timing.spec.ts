import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

const { createAlertRule, listAlertRules } = vi.hoisted(() => ({
  createAlertRule: vi.fn(),
  listAlertRules: vi.fn(),
}))

vi.mock('@vueuse/core', () => ({ useMediaQuery: () => ref(true) }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/admin/ops')>()
  return {
    ...actual,
    opsAPI: { listAlertRules, createAlertRule },
  }
})
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))

// 用原生 <select> 替换自定义 Select，便于直接驱动 v-model 并断言值。
vi.mock('@/components/common/Select.vue', () => ({
  default: {
    props: ['modelValue', 'options', 'error', 'searchable', 'placeholder', 'disabled'],
    emits: ['update:modelValue'],
    template: `
      <select
        :value="modelValue"
        @change="$emit('update:modelValue', isNumeric ? Number($event.target.value) : $event.target.value)"
      >
        <option v-for="option in (options || [])" :key="option.value" :value="option.value" :disabled="option.disabled">
          {{ option.label }}
        </option>
      </select>
    `,
    computed: {
      isNumeric(): boolean {
        return (this.options || []).some((option: { value: unknown }) => typeof option.value === 'number')
      },
    },
  },
}))

import OpsAlertRulesCard from '../OpsAlertRulesCard.vue'

const mountCard = () =>
  mount(OpsAlertRulesCard, {
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        HelpTooltip: true,
      },
    },
  })

async function openCreate() {
  const wrapper = mountCard()
  await flushPromises()
  await wrapper.findAll('button').find((b) => b.text().includes('admin.ops.alertRules.create'))!.trigger('click')
  await flushPromises()
  return wrapper
}

const selects = (wrapper: ReturnType<typeof mountCard>) => wrapper.findAll('select')
// 表单内 select 顺序：metric, operator, groupId, severity, window_minutes
const metricSelect = (wrapper: ReturnType<typeof mountCard>) => selects(wrapper)[0]!
const windowSelect = (wrapper: ReturnType<typeof mountCard>) => selects(wrapper)[4]!
const thresholdInput = (wrapper: ReturnType<typeof mountCard>) =>
  wrapper.findAll('input[type="number"]').find((input) => !input.attributes('min') && !input.attributes('max'))!
const enabledCheckbox = (wrapper: ReturnType<typeof mountCard>) =>
  wrapper.findAll('input[type="checkbox"]')[0]!

describe('OpsAlertRulesCard request-timing metrics', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAlertRules.mockResolvedValue([])
  })

  it('offers the four request-timing metrics in the dropdown', async () => {
    const wrapper = await openCreate()
    const values = metricSelect(wrapper).findAll('option').map((o) => o.attributes('value'))
    expect(values).toContain('upload_ms_p90')
    expect(values).toContain('upload_kbps_p50')
    expect(values).toContain('large_body_ratio')
    expect(values).toContain('real_ttfb_ms_p95')
  })

  it('forces a 1-minute window and applies template defaults when a timing metric is picked', async () => {
    const wrapper = await openCreate()
    await metricSelect(wrapper).setValue('upload_ms_p90')
    await flushPromises()

    expect(windowSelect(wrapper).element.value).toBe('1')
    expect(windowSelect(wrapper).findAll('option')).toHaveLength(1)
    expect((thresholdInput(wrapper).element as HTMLInputElement).value).toBe('8000')
    expect(wrapper.text()).toContain('admin.ops.alertRules.hints.timingWindowFixed')

    const sustained = wrapper.get('input[min="1"][max="1440"]')
    expect((sustained.element as HTMLInputElement).value).toBe('5')

    const enabled = enabledCheckbox(wrapper)
    expect((enabled.element as HTMLInputElement).checked).toBe(false)

    const numericValues = wrapper
      .findAll('input[type="number"]')
      .map((input) => (input.element as HTMLInputElement).value)
    expect(numericValues).toContain('30')
    expect(numericValues).toContain('0.95')
    expect(numericValues).toContain('2')
  })

  it('rejects a large_body_ratio threshold outside 0..1 before submitting', async () => {
    const wrapper = await openCreate()
    await metricSelect(wrapper).setValue('large_body_ratio')
    await flushPromises()

    await thresholdInput(wrapper).setValue('1.5')
    const save = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await save.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.ops.alertRules.validation.ratioRange')
    expect(createAlertRule).not.toHaveBeenCalled()
  })

  it('submits timing filters with the documented defaults', async () => {
    const wrapper = await openCreate()
    await wrapper.get('input[type="text"]').setValue('Upload slow')
    await metricSelect(wrapper).setValue('upload_kbps_p50')
    await flushPromises()

    const save = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await save.trigger('click')
    await flushPromises()

    expect(createAlertRule).toHaveBeenCalledWith(
      expect.objectContaining({
        metric_type: 'upload_kbps_p50',
        window_minutes: 1,
        sustained_minutes: 10,
        threshold: 500,
        operator: '<',
        enabled: false,
        notify_email: false,
        filters: expect.objectContaining({ min_samples: 30, min_coverage: 0.95, recovery_minutes: 2 }),
      })
    )
  })

  it('back-fills filters and window when editing an older timing rule', async () => {
    listAlertRules.mockResolvedValue([
      {
        id: 7,
        name: 'Legacy upload rule',
        description: null,
        metric_type: 'real_ttfb_ms_p95',
        operator: '>',
        threshold: 20000,
        severity: 'P2',
        enabled: true,
        window_minutes: 5,
        sustained_minutes: 5,
        cooldown_minutes: 0,
        // Old rules predate the filters block entirely.
        filters: {},
        notify_email: true,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: null,
      },
    ])
    const wrapper = mountCard()
    await flushPromises()
    await wrapper.findAll('button').find((b) => b.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    expect(windowSelect(wrapper).element.value).toBe('1')
    const numericValues = wrapper
      .findAll('input[type="number"]')
      .map((input) => (input.element as HTMLInputElement).value)
    expect(numericValues).toContain('30')
    expect(numericValues).toContain('0.95')
    expect(numericValues).toContain('2')
    expect(wrapper.text()).not.toContain('admin.ops.alertRules.validation.timingWindowFixed')
  })

  it('flags a retired p95 rule as unsupported and refuses to save it', async () => {
    listAlertRules.mockResolvedValue([
      {
        id: 42,
        name: 'Legacy p95',
        description: null,
        metric_type: 'p95_latency_ms',
        operator: '>',
        threshold: 1000,
        severity: 'warning',
        enabled: true,
        window_minutes: 5,
        sustained_minutes: 3,
        cooldown_minutes: 10,
        filters: {},
        notify_email: true,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: null,
      },
    ])
    const wrapper = mountCard()
    await flushPromises()
    await wrapper.findAll('button').find((b) => b.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.ops.alertRules.validation.metricUnsupported')
    const save = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await save.trigger('click')
    await flushPromises()
    expect(createAlertRule).not.toHaveBeenCalled()
  })


  it('shows the persisted evaluation and never renders an unknown snapshot as healthy', async () => {
    listAlertRules.mockResolvedValue([
      {
        id: 11,
        name: 'upload p90',
        enabled: true,
        metric_type: 'upload_ms_p90',
        operator: '>',
        threshold: 8000,
        window_minutes: 1,
        sustained_minutes: 5,
        severity: 'P2',
        cooldown_minutes: 0,
        notify_email: false,
        evaluation: {
          state: 'unknown',
          reason: 'histogram_boundary_unconfirmed',
          last_evaluated_at: '2026-05-01T10:07:00Z',
          sample_count: 0,
          coverage: null,
        },
      },
      {
        id: 12,
        name: 'no snapshot',
        enabled: true,
        metric_type: 'real_ttfb_ms_p95',
        operator: '>',
        threshold: 20000,
        window_minutes: 1,
        sustained_minutes: 5,
        severity: 'P2',
        cooldown_minutes: 0,
        notify_email: false,
      },
    ])
    const wrapper = mountCard()
    await flushPromises()

    const unknown = wrapper.get('[data-testid="rule-evaluation-11"]')
    expect(unknown.text()).toBe('admin.ops.alertRules.evaluationStates.unknown')
    expect(unknown.classes().join(' ')).not.toContain('emerald')
    expect(unknown.attributes('title')).toContain('admin.ops.alertRules.evaluationReason')

    const missing = wrapper.get('[data-testid="rule-evaluation-12"]')
    expect(missing.text()).toBe('admin.ops.alertRules.evaluationStates.missing')
    expect(missing.classes().join(' ')).not.toContain('emerald')
  })
})
