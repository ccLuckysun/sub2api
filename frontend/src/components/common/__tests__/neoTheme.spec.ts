// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import postcss from 'postcss'

const frontend = fileURLToPath(new URL('../../../../', import.meta.url))
const read = (path: string) => readFileSync(`${frontend}${path}`, 'utf8')
const theme = read('src/styles/neo-theme.css')
const styles = read('src/style.css')
const ast = postcss.parse(theme)

function tokensFor(accent: 'yellow' | 'pink' | 'blue', dark = false) {
  const selectors = [':root']
  if (accent !== 'yellow') selectors.push(`:root[data-accent='${accent}']`)
  if (dark) selectors.push(':root.dark')
  if (dark && accent !== 'yellow') selectors.push(`:root.dark[data-accent='${accent}']`)
  const values: Record<string, string> = {}
  for (const selector of selectors) {
    ast.walkRules(selector, rule => {
      rule.walkDecls(decl => { values[decl.prop] = decl.value })
    })
  }
  return values
}

function luminance(value: string) {
  const channels = value.startsWith('#')
    ? value.slice(1).match(/../g)!.map(channel => parseInt(channel, 16))
    : value.split(' ').map(Number)
  const [r, g, b] = channels.map(channel => {
    const normalized = channel / 255
    return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(first: string, second: string) {
  const values = [luminance(first), luminance(second)].sort((a, b) => b - a)
  return (values[0] + 0.05) / (values[1] + 0.05)
}

describe('D appearance foundations', () => {
  it('provides every binding appearance token', () => {
    const values = tokensFor('yellow')
    for (const name of ['canvas', 'surface', 'ink', 'muted', 'border', 'accent', 'accent-ink', 'pink', 'blue', 'shadow', 'radius']) {
      expect(values[`--neo-${name}`], name).toBeTruthy()
    }
    expect(values['--neo-canvas']).toBe('#fff9e9')
    expect(values['--neo-surface']).toBe('#ffffff')
    expect(values['--neo-ink']).toBe('#111111')
  })

  it.each(['yellow', 'pink', 'blue'] as const)('keeps %s accent and primary text legible in both themes', accent => {
    for (const dark of [false, true]) {
      const values = tokensFor(accent, dark)
      expect(contrast(values['--neo-accent'], values['--neo-accent-ink'])).toBeGreaterThanOrEqual(7)
      for (const surface of ['--neo-canvas', '--neo-surface', '--neo-surface-soft']) {
        expect(contrast(values['--neo-ink'], values[surface])).toBeGreaterThanOrEqual(7)
        expect(contrast(values['--neo-muted'], values[surface])).toBeGreaterThanOrEqual(4.5)
        for (const shade of [400, 500, 600]) {
          expect(contrast(values[`--neo-primary-${shade}`], values[surface])).toBeGreaterThanOrEqual(4.5)
        }
      }
    }
  })

  it('uses runtime RGB channels without sacrificing Tailwind opacity modifiers', () => {
    expect(read('tailwind.config.js')).toContain('rgb(var(--neo-primary-${shade}) / <alpha-value>)')
    for (const shade of [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]) {
      expect(tokensFor('yellow')[`--neo-primary-${shade}`]).toMatch(/^\d+ \d+ \d+$/)
    }
    expect(styles.startsWith("@import './styles/neo-theme.css';")).toBe(true)
  })

  it('supports global motion off and system reduced motion', () => {
    expect(theme).toContain("html[data-motion='off'] *")
    expect(theme).toContain('@media (prefers-reduced-motion: reduce)')
    expect(theme).toContain('animation-iteration-count: 1 !important')
    expect(styles).toContain('@media (prefers-reduced-motion: no-preference)')
    expect(styles).not.toMatch(/\.btn:active:not\(:disabled\)\s*\{[^}]*transform:/s)
  })

  it('uses the centralized runtime and exposes collapsed appearance preferences', () => {
    const sidebar = read('src/components/layout/AppSidebar.vue')
    expect(sidebar).toContain('const { isDark, toggleTheme, allowUserOverride } = useAppearance()')
    expect(sidebar).toContain('v-if="allowUserOverride"')
    expect(sidebar).toContain('@click="toggleTheme()"')
    expect(sidebar).toContain('<AppearancePreferences :collapsed="sidebarCollapsed"')
    expect(sidebar).not.toContain('localStorage')
    expect(sidebar).not.toContain('document.documentElement.classList')
  })

  it('protects virtual row geometry from compact density rules', () => {
    const table = read('src/components/common/DataTable.vue')
    expect(table).toContain("'is-virtualized': shouldVirtualize")
    expect(table).toContain(".table-wrapper:not(.is-virtualized) td:not([colspan])")
    expect(table).toContain('.table-wrapper.is-virtualized { --neo-table-py: 16px;')
    expect(table).toContain("height: virtualPaddingTop + 'px', padding: 0")
    expect(table).toContain("height: virtualPaddingBottom + 'px', padding: 0")
  })
})
