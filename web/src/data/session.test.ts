import { describe, expect, it } from 'vitest'

import { readSessionToken } from './session'

function documentWith(head: string): Document {
  return new DOMParser().parseFromString(`<!doctype html><html><head>${head}</head><body></body></html>`, 'text/html')
}

describe('readSessionToken', () => {
  it('从 Gateway 注入的 meta 里读出令牌', () => {
    const token = 'test-session-token-4c1f0a9e'
    const from = documentWith(`<meta name="opendelo-session-token" content="${token}">`)

    expect(readSessionToken(from)).toBe(token)
  })

  it('没有注入时返回空串，由调用方决定如何呈现', () => {
    // 直接抛异常会让首屏白屏，而这时 Console 其实已经加载出来了。
    expect(readSessionToken(documentWith('<title>OpenDelo</title>'))).toBe('')
  })

  it('meta 存在但没有 content 时同样返回空串', () => {
    expect(readSessionToken(documentWith('<meta name="opendelo-session-token">'))).toBe('')
  })

  it('不把别的 meta 当成令牌', () => {
    const from = documentWith('<meta name="viewport" content="width=device-width">')

    expect(readSessionToken(from)).toBe('')
  })
})
