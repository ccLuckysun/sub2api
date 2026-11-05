import { computed } from 'vue'
import type { Appearance } from '@stripe/stripe-js'
import { useAppearance } from '@/composables/useAppearance'
import { useChartTheme } from '@/composables/useChartTheme'

/** Style the provider-owned iframe only through Stripe's supported API. */
export function useStripeAppearance() {
  const { isDark, effective, motion } = useAppearance()
  const { colors } = useChartTheme()
  return computed<Appearance>(() => {
    const palette = colors.value
    const ring = typeof document !== 'undefined' && typeof getComputedStyle !== 'undefined'
      ? getComputedStyle(document.documentElement).getPropertyValue('--ui-ring').trim()
      : ''
    const focus = ring || (isDark.value ? palette.accent : {
      yellow: '#785C42', pink: '#694F8C', blue: '#2E5E6F'
    }[effective.value.accent])
    return {
      theme: isDark.value ? 'night' : 'stripe',
      variables: {
        colorPrimary: focus,
        colorBackground: palette.surface,
        colorText: palette.text,
        colorTextSecondary: palette.muted,
        colorTextPlaceholder: palette.muted,
        colorDanger: isDark.value ? '#f87171' : '#b91c1c',
        borderRadius: '9px',
        fontFamily: '"Helvetica Neue", "Segoe UI", "PingFang SC", sans-serif'
      },
      rules: {
        '.Input': {
          border: `1px solid ${palette.border}`,
          boxShadow: 'none',
          transition: motion.value === 'off' ? 'none' : 'box-shadow 120ms ease'
        },
        '.Input:focus': { borderColor: focus, boxShadow: `0 0 0 1px ${focus}` },
        '.Tab': { border: `1px solid ${palette.border}`, boxShadow: 'none' },
        '.Tab--selected': {
          backgroundColor: palette.accent,
          color: '#191715',
          borderColor: palette.border,
          boxShadow: 'none'
        },
        '.TabIcon--selected': { color: '#191715' },
        '.Label': { fontWeight: '500' }
      }
    }
  })
}
