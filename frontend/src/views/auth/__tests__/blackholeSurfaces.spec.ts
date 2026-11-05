import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import setupPage from '../../setup/SetupWizardView.vue?raw'
import legalPage from '../../public/LegalDocumentView.vue?raw'
import keyUsagePage from '../../KeyUsageView.vue?raw'
import notFoundPage from '../../NotFoundView.vue?raw'
import plazaFilters from '../../../components/modelPlaza/PlazaFilterBar.vue?raw'
import plazaNav from '../../../components/modelPlaza/PlazaNavBar.vue?raw'
import plazaGroup from '../../../components/modelPlaza/PlazaGroupSection.vue?raw'
import plazaContent from '../../../components/modelPlaza/ModelPlazaContent.vue?raw'

const authStyles = readFileSync(resolve(process.cwd(), 'src/views/auth/auth-pages.css'), 'utf8')
const styles = (source: string) => source.includes('<style')
  ? [...source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map(match => match[1]).join('\n')
  : source

// Source-level contract checks only: these do not claim browser/layout coverage.
describe('public and authentication surface contract', () => {
  it.each([
    ['authentication', authStyles], ['setup', setupPage], ['legal', legalPage],
    ['key usage', keyUsagePage], ['not found', notFoundPage],
    ['plaza filters', plazaFilters], ['plaza navigation', plazaNav],
    ['plaza group', plazaGroup], ['plaza content', plazaContent]
  ])('keeps %s local surfaces free of hard D treatments', (_name, source) => {
    const css = styles(source)
    expect(css).not.toContain('var(--neo-')
    expect(css).not.toMatch(/border(?:-bottom|-top)?:\s*2px solid/)
    expect(css).not.toMatch(/font-weight:\s*[89]\d\d/)
    expect(css).not.toMatch(/box-shadow:\s*\d+px\s+\d+px\s+0\b/)
  })

  it.each([legalPage, keyUsagePage, plazaNav])('preserves configured public branding without a white logo plate', source => {
    expect(source).toContain(':src="siteLogo || \'/logo.svg\'"')
    const logoContainer = source.slice(0, source.indexOf(':src="siteLogo')).split(/<(?:div|span)\b/).at(-1) || ''
    expect(logoContainer).not.toMatch(/bg-white|shadow-|ring-1|rotate-/)
  })

  it('keeps authentication lightweight and does not force the document theme', () => {
    expect(authStyles).toContain('background: var(--ui-canvas)')
    expect(authStyles).not.toMatch(/<canvas|webgl|:root|html\.dark/i)
  })

  it('retains a real error code without the decorative question badge', () => {
    expect(notFoundPage).toContain('aria-hidden="true">404</div>')
    expect(notFoundPage).not.toContain('not-found-code span')
  })
})
