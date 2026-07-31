import { beforeEach, describe, expect, it } from 'vitest'

import { HTML_LANG, nextLanguage, useLanguageStore } from './language'

describe('语言偏好', () => {
  beforeEach(() => {
    localStorage.clear()
    useLanguageStore.setState({ language: 'zh' })
  })

  it('默认中文', () => {
    expect(useLanguageStore.getState().language).toBe('zh')
  })

  it('切换后落盘，刷新不会退回默认值（REQ-UI-008 AC3）', () => {
    useLanguageStore.getState().setLanguage('en')

    expect(useLanguageStore.getState().language).toBe('en')
    expect(localStorage.getItem('opendelo.language')).toBe('en')
  })

  it('开关在两种语言之间来回', () => {
    expect(nextLanguage('zh')).toBe('en')
    expect(nextLanguage('en')).toBe('zh')
  })

  it('每种语言都有对应的 html lang', () => {
    expect(HTML_LANG.zh).toBe('zh-CN')
    expect(HTML_LANG.en).toBe('en')
  })
})
