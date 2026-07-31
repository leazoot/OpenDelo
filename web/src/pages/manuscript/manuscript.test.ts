import { describe, expect, it } from 'vitest'

import type { LedgerEvent } from '../../data/ledgerSummary'
import type { TrustMemory } from '../../data/trustMemories'

import { clockTextOf, loadDraft, saveDraft } from './draft'
import {
  asksEveryTime,
  fromYaml,
  hasPendingChange,
  impactOf,
  manuscriptOf,
  segmentsOf,
  toYaml,
} from './manuscript'

/*
 * 文书的内容模型（REQ-TRUST-006）。
 */

const now = Date.parse('2026-07-30T09:00:00Z')

const memory = (over: Partial<TrustMemory> = {}): TrustMemory => ({
  id: 'tm-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  identity_id: 'id-1',
  service: 'github',
  environment: 'production',
  risk_ceiling: 'medium',
  approval_behavior: 'auto_allow',
  created_from: 'ap-1',
  status: 'active',
  invalidation_reason: '',
  expires_at: '2026-08-30T09:00:00Z',
  created_at: '2026-07-26T09:15:30.123Z',
  ...over,
})

const event = (over: Partial<LedgerEvent> = {}): LedgerEvent => ({
  id: 'ev-1',
  type: 'decision.auto_allowed',
  verdict: 'allow',
  outcome: 'succeeded',
  service: 'github',
  agent_id: 'ag-1',
  created_at: '2026-07-29T09:00:00Z',
  ...over,
})

describe('YAML 与文书视图往返等价（AC4）', () => {
  it('转过去再读回来，每个字段都还在', () => {
    const original = manuscriptOf(memory())

    expect(fromYaml(toYaml(original))).toEqual(original)
  })

  it('空值与带空白的值也能原样回来', () => {
    const original = manuscriptOf(memory({ environment: '', service: ' spaced ' }))

    expect(fromYaml(toYaml(original))).toEqual(original)
  })

  it('缺一个键就当作读不懂，而不是补一个默认值', () => {
    const text = toYaml(manuscriptOf(memory()))
      .split('\n')
      .filter((line) => !line.startsWith('behavior:'))
      .join('\n')

    expect(fromYaml(text)).toBeNull()
  })

  it('认不出的行不当作注释跳过', () => {
    expect(fromYaml('这不是一行 YAML')).toBeNull()
  })

  it('一份别的地方都合法的 YAML，混进一行读不懂的也整份作废', () => {
    const text = `${toYaml(manuscriptOf(memory()))}\n这一行读不懂`

    expect(fromYaml(text)).toBeNull()
  })
})

describe('这份文书现在说什么', () => {
  it('认不出的行为按「要当面确认」处理', () => {
    expect(asksEveryTime('auto_allow')).toBe(false)
    expect(asksEveryTime('always_ask')).toBe(true)
    expect(asksEveryTime('')).toBe(true)
  })

  it('已经是当面确认的文书没有可签署的改动', () => {
    expect(hasPendingChange('auto_allow', 'always_ask')).toBe(true)
    expect(hasPendingChange('always_ask', 'always_ask')).toBe(false)
    // 反方向不成立：签一次「直接通行」是放宽，端点也不接受。
    expect(hasPendingChange('always_ask', 'auto_allow')).toBe(false)
  })
})

describe('影响预览（AC2）', () => {
  it('三个数按结论分开数', () => {
    const impact = impactOf(
      [
        event({ id: 'e1', verdict: 'allow' }),
        event({ id: 'e2', verdict: 'require_approval' }),
        event({ id: 'e3', verdict: 'deny' }),
        event({ id: 'e4', verdict: 'allow' }),
      ],
      manuscriptOf(memory()),
      now,
      200,
    )

    expect(impact).toMatchObject({ passed: 2, confirmed: 1, refused: 1 })
  })

  it('别的服务的记录不算进来 —— 那会让「签下去会发生什么」得到一个更大的错答案', () => {
    const impact = impactOf(
      [event({ service: 'cloudflare' }), event({ id: 'e2' })],
      manuscriptOf(memory()),
      now,
      200,
    )

    expect(impact.passed).toBe(1)
  })

  it('7 天之外的不算', () => {
    const impact = impactOf([event({ created_at: '2026-07-01T09:00:00Z' })], manuscriptOf(memory()), now, 200)

    expect(impact.passed).toBe(0)
  })

  it('窗被填满时报告这三个数是下界', () => {
    const events = Array.from({ length: 3 }, (_ignored, index) => event({ id: `e${String(index)}` }))

    expect(impactOf(events, manuscriptOf(memory()), now, 3).isPartial).toBe(true)
    expect(impactOf(events, manuscriptOf(memory()), now, 200).isPartial).toBe(false)
  })
})

describe('散文里的槽位', () => {
  it('句子被拆成文字与槽位交替的片段', () => {
    const segments = segmentsOf('当 {agent} 请求 {behavior}。')

    expect(segments.map((segment) => segment.slot)).toEqual(['', 'agent', '', 'behavior', ''])
    expect(segments[0]?.text).toBe('当 ')
    expect(segments[4]?.text).toBe('。')
  })

  it('没有槽位的句子原样是一段文字', () => {
    expect(segmentsOf('一句话')).toEqual([{ key: 't0', text: '一句话', slot: '' }])
  })
})

describe('草稿自动保存', () => {
  it('存下去再读回来还是同一份', () => {
    saveDraft('tm-1', { behavior: 'always_ask', savedAt: '14:31' })

    expect(loadDraft('tm-1')).toEqual({ behavior: 'always_ask', savedAt: '14:31' })
  })

  it('没有草稿时返回 null，而不是一份看起来像草稿的默认值', () => {
    expect(loadDraft('tm-never')).toBeNull()
  })

  it.each([
    ['没有分隔符', 'always_ask'],
    ['行为那一半是空的', ' 14:31'],
  ])('读不懂的草稿（%s）返回 null —— 补一个默认值会让人以为自己起草过什么', (_name, stored) => {
    localStorage.setItem('opendelo.manuscript.tm-broken', stored)

    expect(loadDraft('tm-broken')).toBeNull()
  })

  it('时刻写成 HH:MM', () => {
    expect(clockTextOf(new Date(2026, 6, 30, 14, 31).getTime())).toBe('14:31')
    expect(clockTextOf(new Date(2026, 6, 30, 9, 5).getTime())).toBe('09:05')
  })
})
