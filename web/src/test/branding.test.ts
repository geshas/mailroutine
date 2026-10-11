import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const root = path.resolve(import.meta.dirname, '../..')
const read = (f: string) => readFileSync(path.join(root, f), 'utf8')

describe('branding', () => {
  it('uses the app logo (green rounded square with the mail glyph) as the favicon', () => {
    const html = read('index.html')
    expect(html).toContain('<link rel="icon" type="image/svg+xml" href="/favicon.svg" />')
    const svg = read('public/favicon.svg')
    expect(svg).toContain('<rect width="32" height="32" rx="7" fill="#137b5e"/>')
    expect(svg).toContain('m22 7-8.97 5.7') // lucide "mail" glyph, same as the in-app Logo
    expect(svg).not.toMatch(/<script|onload/i)
  })
})
