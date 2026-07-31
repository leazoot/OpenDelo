import { describe, expect, it } from 'vitest'

import { summarizeAgent } from './agentStats'
import { SUMMARY_WINDOW, type LedgerEvent } from './ledgerSummary'

/*
 * Inspector 的「本 Agent 近 7 天」（设计稿 §01）。
 */

const NOW = Date.parse('2026-07-29T12:00:00.000Z')
const DAY = 24 * 60 * 60_000

const event = (id: string, verdict: string, offsetMs = 0): LedgerEvent => ({
  id,
  type: 'request.decided',
  verdict,
  outcome: 'succeeded',
  service: 'github',
  agent_id: 'ag-1',
  created_at: new Date(NOW + offsetMs).toISOString(),
})

describe('本 Agent 近 7 天', () => {
  it('分别数出通行、需确认与拒绝', () => {
    const stats = summarizeAgent(
      [event('a', 'allow'), event('b', 'require_approval'), event('c', 'deny'), event('d', 'allow')],
      NOW,
    )

    expect(stats.passed).toBe(2)
    expect(stats.confirmed).toBe(1)
    expect(stats.refused).toBe(1)
  })

  it('七天之外的不算进来', () => {
    const stats = summarizeAgent([event('a', 'allow'), event('b', 'allow', -8 * DAY)], NOW)

    expect(stats.passed).toBe(1)
  })

  it('刚好七天之内的算', () => {
    expect(summarizeAgent([event('a', 'allow', -7 * DAY + 1_000)], NOW).passed).toBe(1)
  })

  it('窗被这七天填满时数出来的是下界，界面据此显示 128+', () => {
    const full = Array.from({ length: SUMMARY_WINDOW }, (_, index) => event(`e${String(index)}`, 'allow'))

    expect(summarizeAgent(full, NOW).isPartial).toBe(true)
  })

  it('窗没取满时是确数', () => {
    expect(summarizeAgent([event('a', 'allow')], NOW).isPartial).toBe(false)
  })

  it('窗取满但最老一条已经在七天之外时是确数', () => {
    const full = Array.from({ length: SUMMARY_WINDOW }, (_, index) => event(`e${String(index)}`, 'allow'))
    const withOld = [...full.slice(0, SUMMARY_WINDOW - 1), event('old', 'allow', -9 * DAY)]

    expect(summarizeAgent(withOld, NOW).isPartial).toBe(false)
  })

  it('认不出的时刻不算进来，也不让整个统计崩掉', () => {
    const broken: LedgerEvent = { ...event('x', 'allow'), created_at: 'not a time' }

    expect(summarizeAgent([broken], NOW).passed).toBe(0)
  })
})
