import { describe, expect, it } from 'vitest'

import type { LedgerRecord } from '../../data/ledger'
import { copyFor } from '../../i18n/copy'

import {
  detailsOf,
  filterLedger,
  laneOf,
  revokeStateOf,
  ruleDraftPath,
  timeTextOf,
  verdictTextOf,
} from './ledgerView'

/*
 * 账本条目的筛选与措辞（REQ-AUDIT-003）。
 */

const zh = copyFor('zh')

const record = (over: Partial<LedgerRecord> = {}): LedgerRecord => ({
  id: 'ev-1',
  operation_id: 'op-1',
  type: 'decision.auto_allowed',
  agent_id: 'ag-1',
  device_id: 'dv-000042',
  workspace_id: 'ws-1',
  identity_id: 'id-1',
  service: 'github',
  operation: 'pull_request.create',
  resource: { repo: 'Runcoor/opendelo' },
  resolved_scope: {},
  verdict: 'allow',
  risk_level: 'low',
  lease_id: 'ls-1',
  lease_status: 'active',
  outcome: 'succeeded',
  duration_ms: 42,
  is_redacted: true,
  created_at: '2026-07-30T14:22:07.000Z',
  ...over,
})

describe('四个过滤片', () => {
  it('按当时的决定分，而不是按这次执行成没成功', () => {
    expect(laneOf(record({ verdict: 'allow', outcome: 'failed' }))).toBe('passed')
    expect(laneOf(record({ verdict: 'require_approval' }))).toBe('confirmed')
    expect(laneOf(record({ verdict: 'deny' }))).toBe('refused')
  })

  it('认不出的结论不落进三个片里的任何一个', () => {
    expect(laneOf(record({ verdict: '' }))).toBe('')
    expect(filterLedger([record({ verdict: 'maybe' })], 'passed')).toEqual([])
    expect(filterLedger([record({ verdict: 'maybe' })], 'refused')).toEqual([])
  })

  it('「全部」里一条都不少 —— 包括认不出结论的那些', () => {
    const records = [record({ id: 'a', verdict: 'allow' }), record({ id: 'b', verdict: 'maybe' })]

    expect(filterLedger(records, 'all')).toHaveLength(2)
  })

  it('认不出结论时原样显示事件类型，不折成三个里的一个', () => {
    expect(verdictTextOf(record({ verdict: 'maybe', type: 'lease.expired' }), zh)).toBe('lease.expired')
    expect(verdictTextOf(record({ verdict: 'deny' }), zh)).toBe(zh.ledgerRefused)
  })
})

describe('条目详情', () => {
  it('每条都带设备（REQ-AUDIT-003）', () => {
    const keys = detailsOf(record(), zh).map(([key]) => key)

    expect(keys).toContain(zh.ledgerFieldDevice)
    expect(detailsOf(record(), zh).find(([key]) => key === zh.ledgerFieldDevice)?.[1]).toBe('dv-000042')
  })

  it('空字段不摆出来 —— 一行空的没有信息', () => {
    const keys = detailsOf(record({ identity_id: '', workspace_id: '' }), zh).map(([key]) => key)

    expect(keys).not.toContain(zh.ledgerFieldIdentity)
    expect(keys).not.toContain(zh.ledgerFieldWorkspace)
  })
})

describe('收回该 Lease（AC4）', () => {
  it('生效中的才收得回', () => {
    expect(revokeStateOf(record(), zh).may).toBe(true)
  })

  it('「没签发过」与「已经不在了」是两句不同的话', () => {
    expect(revokeStateOf(record({ lease_id: '' }), zh).why).toBe(zh.ledgerNoLease)
    expect(revokeStateOf(record({ lease_status: 'expired' }), zh).why).toBe(zh.ledgerLeaseGone('expired'))
  })

  it('认不出的状态一律收不回（Fail Closed）', () => {
    expect(revokeStateOf(record({ lease_status: '' }), zh).may).toBe(false)
    expect(revokeStateOf(record({ lease_status: 'unknown' }), zh).may).toBe(false)
  })
})

describe('时刻', () => {
  it('写成 HH:MM:SS（设计稿 §07）', () => {
    // 用本地时刻构造，再让实现按本地时区读回来 —— 用例因此不依赖运行环境的时区。
    const at = new Date(2026, 6, 30, 14, 22, 7).toISOString()

    expect(timeTextOf(at, zh)).toBe('14:22:07')
  })

  it('认不出的时刻照实说，而不是显示成 00:00:00', () => {
    expect(timeTextOf('刚刚', zh)).toBe(zh.ledgerTimeUnknown)
    expect(timeTextOf('', zh)).toBe(zh.ledgerTimeUnknown)
  })
})

describe('据此写规则（AC3）', () => {
  it('预填这条记录的 Scope 三要素', () => {
    expect(ruleDraftPath(record())).toBe(
      '/automation/advanced/draft?agent=ag-1&identity=id-1&service=github',
    )
  })
})
