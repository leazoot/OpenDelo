import { describe, expect, it } from 'vitest'

import {
  approvalListSchema,
  describeResource,
  MAX_PASSAGES,
  mergePassage,
  passageOfApproval,
  pendingCountOf,
  type Passage,
} from './passages'

const decision = (verdict: string, reason = '') => ({
  verdict,
  risk_level: 'medium',
  reason_code: reason,
})

const approval = (overrides: Record<string, unknown> = {}) => ({
  id: 'ap-1',
  status: 'pending',
  created_at: '2026-07-29T00:00:00Z',
  request: {
    id: 'rq-1',
    agent_id: 'ag-1',
    service: 'github',
    operation: 'read',
    resource: { repo: 'runcoor/opendelo', path: 'src/' },
    status: 'awaiting_approval',
    created_at: '2026-07-29T00:00:00Z',
    decision: null,
  },
  decision: null,
  ...overrides,
})

/** 解析一份审批项列表并取出第一条。Zod 的结果是数组，取不到就直接失败。 */
function firstOf(item: Record<string, unknown>) {
  const parsed = approvalListSchema.parse({ items: [item] }).items[0]
  if (parsed === undefined) {
    throw new Error('解析后的列表是空的')
  }
  return parsed
}

const passage = (id: string, overrides: Partial<Passage> = {}): Passage => ({
  id,
  agentId: 'ag-1',
  service: 'github',
  operation: 'read',
  resource: 'src/',
  verdict: 'waiting',
  riskLevel: 'low',
  reason: '',
  approvalId: '',
  availableActions: [],
  at: '2026-07-29T00:00:00Z',
  ...overrides,
})

describe('审批项 → Passage', () => {
  it('带上请求的服务、操作与资源', () => {
    const mapped = passageOfApproval(firstOf(approval()))

    expect(mapped?.service).toBe('github')
    expect(mapped?.operation).toBe('read')
    expect(mapped?.resource).toContain('runcoor/opendelo')
    expect(mapped?.approvalId).toBe('ap-1')
  })

  it('没有决策时是等待中，而不是假装已经有结论', () => {
    const mapped = passageOfApproval(firstOf(approval()))

    expect(mapped?.verdict).toBe('waiting')
  })

  it('自动允许带上它的理由（REQ-DECIDE-001 AC3）', () => {
    const item = approval({ decision: decision('allow', 'trust_memory_match') })
    const mapped = passageOfApproval(firstOf(item))

    expect(mapped?.verdict).toBe('allowed')
    expect(mapped?.reason).toBe('trust_memory_match')
  })

  it('拒绝与取消各自成一态', () => {
    const denied = approval({ decision: decision('deny', 'forbidden') })
    const cancelled = approval({ status: 'cancelled' })

    expect(passageOfApproval(firstOf(denied))?.verdict).toBe('denied')
    expect(passageOfApproval(firstOf(cancelled))?.verdict).toBe(
      'cancelled',
    )
  })

  it('认不出的结论按等待处理，不假装它已经有结果', () => {
    const odd = approval({ decision: decision('something-new') })

    expect(passageOfApproval(firstOf(odd))?.verdict).toBe('waiting')
  })

  it('请求读不回来时不画一张空卡片', () => {
    const orphan = approval({ request: null })

    expect(passageOfApproval(firstOf(orphan))).toBeNull()
  })

  it('响应形状不对时解析失败，而不是当成一条空列表', () => {
    expect(() => approvalListSchema.parse({ items: [{ id: 'ap-1' }] })).toThrow()
    expect(() => approvalListSchema.parse({})).toThrow()
  })
})

describe('资源的描述', () => {
  it('把资源里的字符串字段串成一行', () => {
    expect(describeResource({ repo: 'a/b', path: 'src/' })).toBe('a/b · src/')
  })

  it('字符串资源原样返回，认不出的形状返回空串', () => {
    expect(describeResource('~/notes/')).toBe('~/notes/')
    expect(describeResource(null)).toBe('')
    expect(describeResource(42)).toBe('')
  })
})

describe('把新到的 Passage 并进列表', () => {
  it('新的一条排在最前', () => {
    const merged = mergePassage([passage('rq-1')], passage('rq-2'))

    expect(merged.map((item) => item.id)).toEqual(['rq-2', 'rq-1'])
  })

  it('同一次请求只占一行，后到的结论覆盖先到的', () => {
    // arrival 先到、passage 后到是常态。两条会让缝上出现两个同样的请求。
    const merged = mergePassage([passage('rq-1'), passage('rq-2')], passage('rq-2', { verdict: 'allowed' }))

    expect(merged).toHaveLength(2)
    expect(merged[1]?.verdict).toBe('allowed')
  })

  it('更新一条不会把它顶到最前面', () => {
    const merged = mergePassage([passage('rq-1'), passage('rq-2')], passage('rq-2', { verdict: 'denied' }))

    expect(merged.map((item) => item.id)).toEqual(['rq-1', 'rq-2'])
  })

  it('事件里没有 approval 主键时不把已有的抹掉', () => {
    // 抹掉的后果是那一行从此打不开 Folio。
    const existing = [passage('rq-1', { approvalId: 'ap-1' })]
    const merged = mergePassage(existing, passage('rq-1', { verdict: 'allowed' }))

    expect(merged[0]?.approvalId).toBe('ap-1')
  })

  it('列表有上限，缝前的画面是现在而不是历史', () => {
    let list: Passage[] = []
    for (let index = 0; index < MAX_PASSAGES + 10; index++) {
      list = mergePassage(list, passage(`rq-${String(index)}`))
    }

    expect(list).toHaveLength(MAX_PASSAGES)
    expect(list[0]?.id).toBe(`rq-${String(MAX_PASSAGES + 9)}`)
  })
})

describe('待审批数', () => {
  it('只数等在缝前的那些', () => {
    const list = [passage('a'), passage('b', { verdict: 'allowed' }), passage('c', { verdict: 'denied' })]

    expect(pendingCountOf(list)).toBe(1)
  })
})
