import { describe, expect, it } from 'vitest'

import { isSameDay, ledgerListSchema, RIBBON_SIZE, summarize, SUMMARY_WINDOW, type LedgerEvent } from './ledgerSummary'

const NOW = Date.parse('2026-07-29T12:00:00.000Z')

const event = (id: string, verdict: string, offsetMs = 0): LedgerEvent => ({
  id,
  type: 'request.decided',
  verdict,
  outcome: 'succeeded',
  service: 'github',
  agent_id: 'ag-1',
  created_at: new Date(NOW + offsetMs).toISOString(),
})

describe('账本摘要', () => {
  it('分别数出今日的通行与拒绝', () => {
    const summary = summarize([event('a', 'allow'), event('b', 'deny'), event('c', 'allow')], NOW)

    expect(summary.passed).toBe(2)
    expect(summary.refused).toBe(1)
  })

  it('昨天的不算进今天', () => {
    const summary = summarize([event('a', 'allow'), event('b', 'allow', -26 * 60 * 60_000)], NOW)

    expect(summary.passed).toBe(1)
  })

  it('取回的这一窗被今天填满时，数出来的是下界并如实标出来', () => {
    // 账本没有计数端点。把下界说成确数，就是在界面上撒一个可验证的谎。
    const full = Array.from({ length: SUMMARY_WINDOW }, (_, index) => event(`e${String(index)}`, 'allow'))

    expect(summarize(full, NOW).isPartial).toBe(true)
  })

  it('窗没取满时数出来的就是确数', () => {
    expect(summarize([event('a', 'allow')], NOW).isPartial).toBe(false)
  })

  it('窗取满但最老一条已经是昨天时，今天的数是确数', () => {
    const full = Array.from({ length: SUMMARY_WINDOW }, (_, index) => event(`e${String(index)}`, 'allow'))
    const withYesterday = [...full.slice(0, SUMMARY_WINDOW - 1), event('old', 'allow', -26 * 60 * 60_000)]

    expect(summarize(withYesterday, NOW).isPartial).toBe(false)
  })

  it('条带上只摆最近的几条', () => {
    const many = Array.from({ length: 10 }, (_, index) => event(`e${String(index)}`, 'allow'))

    expect(summarize(many, NOW).recent).toHaveLength(RIBBON_SIZE)
  })

  it('认不出的时刻不算进今天，也不让整个摘要崩掉', () => {
    const broken: LedgerEvent = { ...event('x', 'allow'), created_at: 'not a time' }

    expect(isSameDay('not a time', NOW)).toBe(false)
    expect(summarize([broken], NOW).passed).toBe(0)
  })

  it('响应形状不对时解析失败，而不是当成一本空账本', () => {
    expect(() => ledgerListSchema.parse({ items: [{ id: 'a' }] })).toThrow()
    expect(() => ledgerListSchema.parse({})).toThrow()
  })
})
