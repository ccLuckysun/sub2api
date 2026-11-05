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
      surface: token('--ui-surface', dark ? '#111216' : '#FFFFFF'),
      text: token('--ui-ink', dark ? '#ECE8E1' : '#202126'),
      muted: token('--ui-muted', dark ? '#A6A19A' : '#62636B'),
      grid: token('--ui-border', dark ? '#33353D' : '#D5D6DC'),
      border: token('--ui-border', dark ? '#33353D' : '#D5D6DC'),
      accent: token('--ui-accent', { yellow: '#D5BEA4', pink: '#C5B9EE', blue: '#BAD9E5' }[accent])
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
    borderWidth: 1,
    cornerRadius: 6,
    padding: 10
  }))
  return { colors, animation, tooltip }
}
