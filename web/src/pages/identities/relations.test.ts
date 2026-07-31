import { describe, expect, it } from 'vitest'

import type { Agent } from '../../data/agents'
import type { Identity } from '../../data/identities'
import type { Lease } from '../../data/leases'
import type { TrustMemory } from '../../data/trustMemories'
import { copyFor } from '../../i18n/copy'

import {
  agentCards,
  alreadyHolds,
  destinationCards,
  draftOf,
  signPath,
  type WorkbenchInput,
} from './relations'

/*
 * 两列卡片是四份数据合成出来的（REQ-UI-005）。
 *
 * 合成错了不会报错，只会让页面上的关系看起来很合理 —— 这一组用例守的正是
 * 「哪条 Lease 属于哪处资源」「这处资源现在按什么规则放行」。
 */

const zh = copyFor('zh')
const now = Date.parse('2026-07-30T09:00:00Z')

const agent = (id: string, name: string): Agent => ({
  id,
  name,
  type: 'claude_code',
  device_id: 'dv-000001',
  workspace_id: 'ws-1',
  trust_level: 'known',
  status: 'active',
  last_seen_at: '2026-07-30T08:59:00Z',
})

const identity = (id: string, service: string): Identity => ({
  id,
  service,
  account_label: 'ops@example.com',
  environment: 'production',
  is_default: false,
  status: 'active',
})

const lease = (id: string, agentId: string, identityId: string): Lease => ({
  id,
  agentId,
  identityId,
  service: 'github',
  scope: 'src/',
  expiresAt: '2026-07-30T09:15:00Z',
  isSessionBound: false,
})

const memory = (identityId: string, ceiling: string, status = 'active'): TrustMemory => ({
  id: `tm-${identityId}-${ceiling}-${status}`,
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  identity_id: identityId,
  service: 'github',
  environment: 'production',
  risk_ceiling: ceiling,
  approval_behavior: 'auto_allow',
  created_from: 'ap-1',
  status,
  invalidation_reason: '',
  expires_at: '2026-08-30T09:00:00Z',
  created_at: '2026-07-01T09:00:00Z',
})

const input = (over: Partial<WorkbenchInput> = {}): WorkbenchInput => ({
  agents: [agent('ag-1', 'writer-agent')],
  identities: [identity('id-1', 'github')],
  leases: [],
  memories: [],
  now,
  isHere: true,
  copy: zh,
  ...over,
})

describe('左列 · Agent 卡片', () => {
  it('数的是这个 Agent 自己手上的授权，不是全部', () => {
    const cards = agentCards(
      input({
        agents: [agent('ag-1', 'writer-agent'), agent('ag-2', 'reader-agent')],
        leases: [lease('ls-1', 'ag-1', 'id-1'), lease('ls-2', 'ag-2', 'id-1'), lease('ls-3', 'ag-1', 'id-1')],
      }),
    )

    expect(cards.find((card) => card.id === 'ag-1')?.leaseCount).toBe(2)
    expect(cards.find((card) => card.id === 'ag-2')?.leaseCount).toBe(1)
  })

  it('带上请求来自哪台设备（AC2）', () => {
    expect(agentCards(input())[0]?.deviceId).toBe('dv-000001')
  })
})

describe('右列 · Destination 卡片', () => {
  it('活跃授权按身份归拢 —— 别人的那条不会挂到这一处上', () => {
    const cards = destinationCards(
      input({
        identities: [identity('id-1', 'github'), identity('id-2', 'cloudflare')],
        leases: [lease('ls-1', 'ag-1', 'id-1'), lease('ls-2', 'ag-1', 'id-2')],
      }),
    )

    expect(cards[0]?.holders.map((holder) => holder.leaseId)).toEqual(['ls-1'])
    expect(cards[1]?.holders.map((holder) => holder.leaseId)).toEqual(['ls-2'])
  })

  it('标签上写的是谁拿着它与还剩多久（AC3）', () => {
    const card = destinationCards(input({ leases: [lease('ls-1', 'ag-1', 'id-1')] }))[0]

    expect(card?.holders[0]?.who).toBe('writer-agent')
    expect(card?.holders[0]?.left).toBe('15m')
  })

  it('名册里查不到的持有者退回主键，而不是显示成一个空位', () => {
    const card = destinationCards(input({ agents: [], leases: [lease('ls-1', 'ag-9', 'id-1')] }))[0]

    expect(card?.holders[0]?.who).toBe('ag-9')
  })

  it('规则摘要只数仍然生效的记忆', () => {
    const card = destinationCards(
      input({ memories: [memory('id-1', 'medium'), memory('id-1', 'high', 'invalidated')] }),
    )[0]

    expect(card?.rule).toBe(zh.identitiesRuleSummary(1, 'medium'))
  })

  it('别处的记忆不会算到这一处头上', () => {
    const card = destinationCards(input({ memories: [memory('id-2', 'medium')] }))[0]

    expect(card?.rule).toBe('')
  })

  it('一条记忆也没有时返回空串，由界面说「每次都会问你」而不是在这里编一句默认规则', () => {
    expect(destinationCards(input())[0]?.rule).toBe('')
  })
})

describe('拖放生成的是草稿（REQ-IDENT-003 AC1）', () => {
  it('草稿只记两端，不带任何授权字段', () => {
    const draft = draftOf(agentCards(input())[0] ?? unreachable(), destinationCards(input())[0] ?? unreachable())

    expect(draft).toEqual({
      agentId: 'ag-1',
      agentName: 'writer-agent',
      identityId: 'id-1',
      identityName: 'github/ops@example.com',
    })
    expect(Object.keys(draft)).not.toContain('expiresAt')
  })

  it('草稿走 URL 去签署，因此刷新之后它还在', () => {
    const draft = draftOf(agentCards(input())[0] ?? unreachable(), destinationCards(input())[0] ?? unreachable())

    expect(signPath(draft)).toBe('/automation/advanced/draft?agent=ag-1&identity=id-1')
  })

  it('已经握着授权的组合不必再签一次', () => {
    const card = destinationCards(input({ leases: [lease('ls-1', 'ag-1', 'id-1')] }))[0] ?? unreachable()

    expect(alreadyHolds(card, 'ag-1')).toBe(true)
    expect(alreadyHolds(card, 'ag-2')).toBe(false)
  })
})

/** 用例里的数组下标越界应当当场失败，而不是悄悄换成一个空对象。 */
function unreachable(): never {
  throw new Error('用例的夹具没有构造出这一项')
}
