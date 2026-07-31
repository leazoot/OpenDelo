import { describe, expect, it } from 'vitest'

import { changeOf, folioOf, isDecidable, isSettled, rollbackOf, type FolioPayload } from './folio'

/*
 * 卷宗的取数（REQ-APPROVAL-001）。
 */

const payload = (overrides: Partial<FolioPayload> = {}): FolioPayload => ({
  id: 'rq-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  service: 'cloudflare',
  operation: 'update_dns_record',
  resource: { zone: 'example.com', record: 'www' },
  desired_change: { content: '203.0.113.9' },
  reason: '把 www 指到新机器',
  status: 'awaiting_approval',
  change_preview: null,
  withheld_operations: null,
  created_at: '2026-07-29T09:00:00Z',
  decision: {
    verdict: 'require_approval',
    risk_level: 'medium',
    risk_factors: ['adapter_declared_label', 'production_write'],
    identity_id: 'id-1',
    reason_code: 'requires_confirmation',
    resolved_scope: {
      operation: 'update_dns_record',
      resource: { zone: 'example.com' },
      not_before: '2026-07-29T09:00:00Z',
      expires_at: '2026-07-29T09:15:00Z',
      request_limit: 1,
    },
  },
  ...overrides,
})

describe('期望变更', () => {
  it('逐字段列出要写入的值', () => {
    expect(changeOf({ content: '203.0.113.9', ttl: 300 })).toEqual([
      { field: 'content', value: '203.0.113.9', before: '' },
      { field: 'ttl', value: '300', before: '' },
    ])
  })

  it('读操作没有变更 —— null 得到空数组而不是一个空对象行', () => {
    expect(changeOf(null)).toEqual([])
  })

  it('查勘查到的旧值按字段名对上去', () => {
    const preview = [
      { resource: 'www.example.com', field: 'content', before: '203.0.113.1', after: '203.0.113.9' },
    ]
    expect(changeOf({ content: '203.0.113.9', ttl: 300 }, preview)).toEqual([
      { field: 'content', value: '203.0.113.9', before: '203.0.113.1' },
      // 查勘没查这一项：旧值留空，而不是把别的字段的值填进来。
      { field: 'ttl', value: '300', before: '' },
    ])
  })

  it('没有查勘过时卷宗知道自己没有旧值可说', () => {
    expect(folioOf(payload()).isPreviewed).toBe(false)
    expect(folioOf(payload({ change_preview: [] })).isPreviewed).toBe(true)
  })

  it('仍然关闭的操作照后端给的清单，缺席时为空数组', () => {
    expect(folioOf(payload()).withheldOperations).toEqual([])
    expect(folioOf(payload({ withheld_operations: ['delete_dns_record'] })).withheldOperations).toEqual([
      'delete_dns_record',
    ])
  })
})

describe('能不能还原', () => {
  it('irreversible 在场就是不可还原', () => {
    expect(rollbackOf(['irreversible'], true)).toBe('none')
  })

  it('要写但没有 irreversible，说明 Adapter 声明了可逆', () => {
    expect(rollbackOf(['adapter_declared_label'], true)).toBe('possible')
  })

  it('没有变更就没有需要还原的东西', () => {
    expect(rollbackOf(['read_only'], false)).toBe('not-applicable')
  })

  it('不可还原优先于「没有变更」—— 两者同时出现时不能说成无所谓', () => {
    expect(rollbackOf(['irreversible'], false)).toBe('none')
  })
})

describe('状态', () => {
  it('等待中：能决定，且不显示「已处理」', () => {
    expect(isDecidable('awaiting_approval')).toBe(true)
    expect(isSettled('awaiting_approval')).toBe(false)
  })

  it('已经有结论的那些显示「已处理」，也不能再决定', () => {
    for (const status of ['approved', 'rejected', 'denied', 'expired', 'cancelled', 'succeeded']) {
      expect(isSettled(status)).toBe(true)
      expect(isDecidable(status)).toBe(false)
    }
  })

  it('认不出的状态两边都不认：既不说已处理，也不允许决定', () => {
    // 两个谓词各自向安全的一侧失败。合成一个布尔量必然有一侧要说谎。
    expect(isSettled('resolving')).toBe(false)
    expect(isDecidable('resolving')).toBe(false)
  })
})

describe('卷宗', () => {
  it('把请求、决策与解析后的 Scope 合成一份', () => {
    const folio = folioOf(payload())

    expect(folio.resource).toBe('example.com · www')
    expect(folio.identityId).toBe('id-1')
    expect(folio.riskFactors).toEqual(['adapter_declared_label', 'production_write'])
    expect(folio.scope.expires_at).toBe('2026-07-29T09:15:00Z')
    expect(folio.rollback).toBe('possible')
  })

  it('还没有决策时不编一个风险等级出来', () => {
    const folio = folioOf(payload({ decision: null }))

    expect(folio.riskLevel).toBe('')
    expect(folio.riskFactors).toEqual([])
    expect(folio.scope).toEqual({})
  })
})
