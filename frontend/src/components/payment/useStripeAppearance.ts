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
    const focus = isDark.value ? palette.accent : {
      yellow: '#705400', pink: '#9c275c', blue: '#175ea0'
    }[effective.value.accent]
    return {
      theme: isDark.value ? 'night' : 'stripe',
      variables: {
        colorPrimary: focus,
        colorBackground: palette.surface,
        colorText: palette.text,
        colorTextSecondary: palette.muted,
        colorTextPlaceholder: palette.muted,
        colorDanger: isDark.value ? '#f87171' : '#b91c1c',
        borderRadius: '8px',
        fontFamily: '"Avenir Next", "Segoe UI", sans-serif'
      },
      rules: {
        '.Input': {
          border: `2px solid ${palette.border}`,
          boxShadow: 'none',
          transition: motion.value === 'off' ? 'none' : 'box-shadow 120ms ease'
        },
        '.Input:focus': { borderColor: focus, boxShadow: `3px 3px 0 ${focus}` },
        '.Tab': { border: `2px solid ${palette.border}`, boxShadow: 'none' },
        '.Tab--selected': {
          backgroundColor: palette.accent,
          color: '#111111',
          borderColor: palette.border,
          boxShadow: `3px 3px 0 ${isDark.value ? '#050505' : '#111111'}`
        },
        '.TabIcon--selected': { color: '#111111' },
        '.Label': { fontWeight: '700' }
      }
    }
  })
}
