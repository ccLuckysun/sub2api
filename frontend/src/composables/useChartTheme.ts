import { computed } from 'vue'
import { useAppearance } from '@/composables/useAppearance'

/** Canvas cannot inherit CSS: resolve the same tokens whenever appearance changes.
 * Series colours and business scales deliberately remain owned by each chart. */
export function useChartTheme() {
  const { isDark, effective, motion } = useAppearance()
  const colors = computed(() => {
    const dark = isDark.value
    // Accent changes also invalidate canvas styles, even without a theme change.
    const accent = effective.value.accent
    const css = typeof document !== 'undefined' && typeof getComputedStyle !== 'undefined'
      ? getComputedStyle(document.documentElement)
      : null
    const token = (name: string, fallback: string) => css?.getPropertyValue(name).trim() || fallback
    return {
      surface: token('--neo-surface', dark ? '#252525' : '#ffffff'),
      text: token('--neo-ink', dark ? '#fff9e9' : '#111111'),
      muted: token('--neo-muted', dark ? '#bcb7a9' : '#6d6657'),
      grid: token('--neo-divider', dark ? '#49473f' : '#ded8c9'),
      border: token('--neo-border', dark ? '#eee8d8' : '#111111'),
      accent: token('--neo-accent', { yellow: '#ffe14d', pink: '#ff9ecb', blue: '#9bd0ff' }[accent])
    }
  })
  const animation = computed(() => motion.value === 'off'
    ? false as const
    : { duration: motion.value === 'rich' ? 300 : 120 })
  const tooltip = computed(() => ({
    backgroundColor: colors.value.surface,
    titleColor: colors.value.text,
    bodyColor: colors.value.text,
    borderColor: colors.value.border,
    borderWidth: 2,
    cornerRadius: 6,
    padding: 10
  }))
  return { colors, animation, tooltip }
}
