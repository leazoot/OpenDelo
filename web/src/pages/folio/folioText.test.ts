import { describe, expect, it } from 'vitest'

import { folioOf, type FolioPayload } from '../../data/folio'
import { copyFor } from '../../i18n/copy'

import {
  consequencesOf,
  durationTextOf,
  limitTextOf,
  riskFactorTextOf,
  scopeResourceTextOf,
  withheldOperationsTextOf,
} from './folioText'

/*
 * 卷宗上的措辞。「仍然关闭的权限」是这一段的重点（REQ-APPROVAL-001）。
 */

const zh = copyFor('zh')

const payload = (scope: Record<string, unknown> = {}): FolioPayload => ({
  id: 'rq-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  service: 'cloudflare',
  operation: 'update_dns_record',
  resource: { zone: 'example.com' },
  desired_change: { content: '203.0.113.9' },
  reason: '换机器',
  status: 'awaiting_approval',
  change_preview: null,
  withheld_operations: null,
  created_at: '2026-07-29T09:00:00Z',
  decision: {
    verdict: 'require_approval',
    risk_level: 'medium',
    risk_factors: [],
    identity_id: 'id-1',
    reason_code: 'requires_confirmation',
    resolved_scope: {
      operation: 'update_dns_record',
      resource: { zone: 'example.com' },
      not_before: '2026-07-29T09:00:00Z',
      expires_at: '2026-07-29T09:15:00Z',
      request_limit: 1,
      ...scope,
    },
  },
})

describe('授权期限', () => {
  it('说的是这次授权有多长，不是打开页面时还剩多久', () => {
    expect(durationTextOf(folioOf(payload()).scope, zh)).toBe('15m')
  })

  it('两端有一端认不出就说待定，不拿现在的时刻去补', () => {
    expect(durationTextOf(folioOf(payload({ expires_at: '' })).scope, zh)).toBe(zh.folioDurationUnknown)
  })

  it('结束不晚于开始时同样说待定', () => {
    const inverted = payload({ expires_at: '2026-07-29T08:00:00Z' })

    expect(durationTextOf(folioOf(inverted).scope, zh)).toBe(zh.folioDurationUnknown)
  })
})

describe('次数上限', () => {
  it('有上限时说次数', () => {
    expect(limitTextOf(folioOf(payload()).scope, zh)).toBe(zh.folioLeaseLimit(1))
  })

  it('没有上限时明说不限，不留空', () => {
    expect(limitTextOf(folioOf(payload({ request_limit: 0 })).scope, zh)).toBe(zh.folioLeaseUnlimited)
  })
})

describe('放行之后', () => {
  it('三条否定句都在：资源之外、操作之外、凭据与出站', () => {
    const texts = consequencesOf(folioOf(payload()), zh).map((item) => item.text)

    expect(texts).toContain(zh.folioWithheldResource('example.com'))
    expect(texts).toContain(zh.folioWithheldOperation('update_dns_record'))
    expect(texts).toContain(zh.folioWithheldCredential)
  })

  it('否定句跟着 Scope 走，不是写死的宣传语', () => {
    const narrowed = payload({ resource: { zone: 'other.example' }, operation: 'read_dns_record' })
    const texts = consequencesOf(folioOf(narrowed), zh).map((item) => item.text)

    expect(texts).toContain(zh.folioWithheldResource('other.example'))
    expect(texts).toContain(zh.folioWithheldOperation('read_dns_record'))
  })

  it('可撤销与可回滚各占一条', () => {
    const texts = consequencesOf(folioOf(payload()), zh).map((item) => item.text)

    expect(texts).toContain(zh.folioRevocable)
    expect(texts).toContain(zh.folioRollbackPossible)
  })

  it('能力表给得出清单时逐个点名，剩下的数目照实说', () => {
    const folio = folioOf({
      ...payload(),
      withheld_operations: ['create_dns_record', 'delete_dns_record', 'purge_cache', 'read_dns_record'],
    })

    expect(withheldOperationsTextOf(folio, 'update_dns_record', zh)).toBe(
      zh.folioWithheldOperations(['create_dns_record', 'delete_dns_record', 'purge_cache'], 1),
    )
  })

  it('清单短于上限时不说「另有」', () => {
    const folio = folioOf({ ...payload(), withheld_operations: ['purge_cache'] })

    expect(withheldOperationsTextOf(folio, 'update_dns_record', zh)).toBe(
      zh.folioWithheldOperations(['purge_cache'], 0),
    )
  })

  it('能力表答不上来时退回那句笼统的否定句，而不是留白', () => {
    // 它说的也是真的，只是不完整。留白等于让用户以为没有边界。
    expect(withheldOperationsTextOf(folioOf(payload()), 'update_dns_record', zh)).toBe(
      zh.folioWithheldOperation('update_dns_record'),
    )
  })

  it('放行那一条是唯一的肯定句，其余都是「不获得」', () => {
    const items = consequencesOf(folioOf(payload()), zh)

    expect(items.filter((item) => item.tone === 'granted')).toHaveLength(1)
    expect(items.filter((item) => item.tone === 'withheld')).toHaveLength(3)
  })
})

describe('Scope 里的资源', () => {
  it('Scope 没给资源时退回请求里的那个，不显示空白', () => {
    expect(scopeResourceTextOf(folioOf(payload({ resource: null })))).toBe('example.com')
  })
})

describe('风险因子', () => {
  it('译成人话', () => {
    expect(riskFactorTextOf('bulk_change', zh)).toBe(zh.factorBulkChange)
  })

  it('认不出的码原样显示，不假装它不存在', () => {
    expect(riskFactorTextOf('some_new_factor', zh)).toBe('some_new_factor')
  })
})
