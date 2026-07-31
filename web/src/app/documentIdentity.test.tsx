import { render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { copyFor } from '../i18n/copy'

import { boundaryStateOf, documentTitleOf, faviconOf, useDocumentIdentity } from './documentIdentity'

const zh = copyFor('zh')
const en = copyFor('en')

/*
 * 标签页的身份（REQ-UI-003 AC4）。
 */

describe('标签标题', () => {
  it('带上待审批数', () => {
    expect(documentTitleOf('Gate', 2, zh)).toBe('OpenDelo — Gate · 2 待审批')
  })

  it('没有待审批时不带后半段，而不是显示 0 待审批', () => {
    expect(documentTitleOf('Gate', 0, zh)).toBe('OpenDelo — Gate')
  })

  it('随语言变化', () => {
    expect(documentTitleOf('Ledger', 3, en)).toBe('OpenDelo — Ledger · 3 pending')
  })
})

describe('favicon 的状态', () => {
  it('有人在等时是等待色', () => {
    expect(boundaryStateOf('connected', 2)).toBe('pending')
  })

  it('连不上时先说连不上，不被旧的待审批数盖住', () => {
    // 断开时手里的数字是上一次读到的。用它盖住「联系不上 Gateway」
    // 会让标签看起来一切正常。
    expect(boundaryStateOf('disconnected', 2)).toBe('disconnected')
  })

  it('一切正常时是生效色', () => {
    expect(boundaryStateOf('connected', 0)).toBe('connected')
  })

  it('还在连的时候不借用等待色', () => {
    expect(boundaryStateOf('connecting', 0)).toBe('connecting')
  })
})

/**
 * 取色的结果在这里只是一个透明的字符串。
 *
 * 不写成真实色值：令牌扫描（scripts/check-tokens.mjs）会把它当成组件里的字面色值，
 * 而那条检查正是要守住的东西。
 */
const RESOLVED_COLOR = 'resolved-token-color'

describe('favicon 的图形', () => {
  it('是一段带颜色的缝，且是 data URI 不发外部请求', () => {
    const icon = faviconOf(RESOLVED_COLOR)

    expect(icon.startsWith('data:image/svg+xml,')).toBe(true)
    expect(decodeURIComponent(icon)).toContain(RESOLVED_COLOR)
    expect(decodeURIComponent(icon)).toContain('<rect')
  })
})

describe('挂到文档上', () => {
  afterEach(() => {
    document.head.innerHTML = ''
    document.documentElement.lang = ''
  })

  function Probe({ pending, color }: { pending: number; color: string }) {
    useDocumentIdentity({
      page: 'Gate',
      pending,
      state: boundaryStateOf('connected', pending),
      language: 'en',
      copy: en,
      theme: 'dark',
      resolveColor: () => color,
    })
    return null
  }

  it('写入标题、语言与 favicon', () => {
    render(<Probe pending={2} color={RESOLVED_COLOR} />)

    expect(document.title).toBe('OpenDelo — Gate · 2 pending')
    expect(document.documentElement.lang).toBe('en')

    const icon = document.head.querySelector('link[rel="icon"]')
    expect(icon).not.toBeNull()
    expect(icon?.getAttribute('href')).toContain(encodeURIComponent(RESOLVED_COLOR))
  })

  it('令牌取不到时不动 favicon，而不是画一个没有颜色的图标', () => {
    render(<Probe pending={0} color="" />)

    expect(document.head.querySelector('link[rel="icon"]')).toBeNull()
  })
})
