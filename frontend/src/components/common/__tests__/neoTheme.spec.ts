// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import loadConfig from 'tailwindcss/loadConfig'

const frontend = fileURLToPath(new URL('../../../../', import.meta.url))
const read = (path: string) => readFileSync(`${frontend}${path}`, 'utf8')
const theme = read('src/styles/theme.css')
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
  const resolve = (value: string): string => value.replace(/var\((--[\w-]+)\)/g, (_, name) => resolve(values[name]))
  return Object.fromEntries(Object.entries(values).map(([key, value]) => [key, resolve(value)]))
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

describe('Black-hole appearance foundations', () => {
  it('provides every binding appearance token', () => {
    const values = tokensFor('yellow')
    for (const name of ['canvas', 'surface', 'surface-raised', 'ink', 'muted', 'border', 'control-border', 'accent', 'accent-ink', 'ring', 'shadow', 'radius', 'duration']) {
      expect(values[`--ui-${name}`], name).toBeTruthy()
    }
    expect(values['--ui-canvas']).toBe('#F5F5F6')
    expect(values['--ui-surface']).toBe('#FFFFFF')
    expect(values['--ui-ink']).toBe('#202126')
  })

  it.each(['yellow', 'pink', 'blue'] as const)('keeps %s accent and primary text legible in both themes', accent => {
    for (const dark of [false, true]) {
      const values = tokensFor(accent, dark)
      expect(contrast(values['--ui-accent'], values['--ui-accent-ink'])).toBeGreaterThanOrEqual(7)
      for (const surface of ['--ui-canvas', '--ui-surface', '--ui-surface-raised']) {
        expect(contrast(values['--ui-ink'], values[surface])).toBeGreaterThanOrEqual(7)
        expect(contrast(values['--ui-muted'], values[surface])).toBeGreaterThanOrEqual(4.5)
        for (const shade of [400, 500, 600]) {
          expect(contrast(values[`--ui-primary-${shade}`], values[surface])).toBeGreaterThanOrEqual(4.5)
        }
        expect(contrast(values['--ui-ring'], values[surface])).toBeGreaterThanOrEqual(3)
        expect(contrast(values['--ui-control-border'], values[surface])).toBeGreaterThanOrEqual(3)
      }
      // Both the explicit accent control and the legacy primary/white bridge.
      for (const shade of [500, 600]) {
        expect(contrast(values[`--ui-primary-${shade}`], dark ? values['--ui-accent-ink'] : '#FFFFFF')).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('binds functional control outlines without strengthening decorative borders', () => {
    const declarations = (source: string, selector: string) => {
      const values: Record<string, string> = {}
      postcss.parse(source).walkRules(selector, rule => {
        rule.walkDecls(decl => { values[decl.prop] = decl.value })
      })
      return values
    }
    expect(declarations(styles, '.input').border).toBe('1px solid var(--ui-control-border)')
    expect(declarations(styles, '.card, .glass-card, .card-glass').border).toBe('1px solid var(--ui-border)')
    expect(declarations(styles, '.switch').border).toBe('1px solid var(--ui-control-border)')
    for (const file of ['Select.vue', 'ProxySelector.vue', 'DateRangePicker.vue']) {
      const source = read(`src/components/common/${file}`)
      const css = source.slice(source.indexOf('<style')).replace(/<\/?style[^>]*>/g, '')
      const trigger = file === 'DateRangePicker.vue' ? '.date-picker-trigger' : '.select-trigger'
      expect(declarations(css, trigger).border, file).toBe('1px solid var(--ui-control-border)')
      if (file === 'DateRangePicker.vue') {
        expect(declarations(css, '.date-picker-input').border).toBe('1px solid var(--ui-control-border)')
      }
    }
    expect(read('src/components/common/GroupSelector.vue')).toContain('border-ui-control')
    expect(read('src/components/common/Toggle.vue')).toContain('border-color: var(--ui-control-border)')
    expect(theme).toContain('.border-ui-control { border-color: var(--ui-control-border); }')
  })

  it('keeps the approved palette and every compatibility alias', () => {
    expect(tokensFor('yellow', true)).toMatchObject({
      '--ui-canvas': '#08090B', '--ui-surface': '#111216',
      '--ui-ink': '#ECE8E1', '--ui-muted': '#A6A19A', '--ui-accent': '#D5BEA4'
    })
    expect(tokensFor('pink')['--ui-accent']).toBe('#C5B9EE')
    expect(tokensFor('blue')['--ui-accent']).toBe('#BAD9E5')
    for (const dark of [false, true]) {
      for (const accent of ['yellow', 'pink', 'blue'] as const) {
        const values = tokensFor(accent, dark)
        for (const name of ['canvas', 'surface', 'ink', 'muted', 'border', 'accent', 'accent-ink', 'shadow', 'radius', 'duration']) {
          expect(values[`--neo-${name}`]).toBe(values[`--ui-${name}`])
        }
        expect(values['--neo-surface-soft']).toBe(values['--ui-surface-raised'])
        expect(values['--neo-focus']).toBe(values['--ui-ring'])
        for (const shade of [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]) {
          expect(values[`--neo-primary-${shade}`]).toBe(values[`--ui-primary-${shade}`])
        }
      }
    }
  })

  it('removes brutalist borders, offset shadows and excessive font weights', () => {
    for (const source of [styles, read('src/styles/announcement-markdown.css'), read('src/styles/onboarding.css')]) {
      expect(source).not.toMatch(/border(?:-top|-bottom)?: 2px solid/)
      expect(source).not.toMatch(/\dpx \dpx 0 var\(/)
      expect(source).not.toMatch(/font-(?:bold|extrabold|black)|font-weight: [7-9]\d\d/)
    }
    expect(read('src/components/layout/AppLayout.vue')).not.toContain('radial-gradient')
    expect(read('src/components/layout/AuthLayout.vue')).not.toContain('rotate(-3deg)')
  })

  it('preserves selected desktop and mobile rows, including opaque sticky cells', () => {
    const table = read('src/components/common/DataTable.vue')
    expect(table).toContain("'table-row-selected': selectable && isRowSelected(item.row, item.index)")
    expect(table).toContain('tbody tr.table-row-selected:hover .sticky-col')
    expect(table).toContain('.neo-mobile-row-selected { background: color-mix(')
    expect(table).toContain('position: sticky')
  })

  it('uses runtime RGB channels without sacrificing Tailwind opacity modifiers', () => {
    expect(read('tailwind.config.js')).toContain('rgb(var(--ui-primary-${shade}) / <alpha-value>)')
    for (const shade of [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]) {
      expect(tokensFor('yellow')[`--ui-primary-${shade}`]).toMatch(/^\d+ \d+ \d+$/)
    }
    expect(styles.startsWith("@import './styles/theme.css';")).toBe(true)
  })

  it('keeps neutral utility text readable on the raised surfaces', () => {
    const colors = loadConfig(`${frontend}tailwind.config.js`).theme?.extend?.colors as Record<string, Record<number, string>>
    for (const shade of [400, 500, 600]) {
      expect(contrast(colors.gray[shade], tokensFor('yellow')['--ui-surface-raised'])).toBeGreaterThanOrEqual(4.5)
    }
    for (const shade of [400, 500]) {
      expect(contrast(colors.dark[shade], tokensFor('yellow', true)['--ui-surface-raised'])).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('compiles runtime accents and opacity modifiers from the real Tailwind config', async () => {
    const config = loadConfig(`${frontend}tailwind.config.js`)
    const result = await postcss([tailwindcss({
      ...config,
      content: [{ raw: 'bg-primary-500/20 text-primary-600/80 dark:bg-primary-900/10' }]
    })]).process('@tailwind utilities;', { from: undefined })
    expect(result.css).toContain('rgb(var(--ui-primary-500) / 0.2)')
    expect(result.css).toContain('rgb(var(--ui-primary-600) / 0.8)')
    expect(result.css).toContain('rgb(var(--ui-primary-900) / 0.1)')
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
    expect(table).toContain('.table-wrapper.is-virtualized { --ui-table-py: 16px;')
    expect(table).toContain("height: virtualPaddingTop + 'px', padding: 0")
    expect(table).toContain("height: virtualPaddingBottom + 'px', padding: 0")
  })
})
