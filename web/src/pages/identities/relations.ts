import type { Agent } from '../../data/agents'
import type { Identity } from '../../data/identities'
import { formatRemaining, remainingMillis, type Lease } from '../../data/leases'
import { activeMemories, type TrustMemory } from '../../data/trustMemories'
import type { Copy } from '../../i18n/copy'

/*
 * Identities 是一张关系图，不是一张密码表（REQ-UI-005 AC1）。
 *
 * 这里把四份服务端数据（Agent、身份、Lease、Trust Memory）合成两列卡片。
 * 合成写成纯函数，是因为「哪条 Lease 属于哪处资源」「这处资源现在按什么规则
 * 放行」这两件事错了不会有任何报错，只会让页面上的关系看起来很合理。
 */

/** 左列：一个 Agent 现在与缝的关系。 */
export interface AgentCard {
  readonly id: string
  readonly name: string
  readonly kind: string
  readonly deviceId: string
  /** 请求来自你正坐在的这台机器（假设 A-10）。 */
  readonly isHere: boolean
  readonly trustLevel: string
  readonly status: string
  readonly leaseCount: number
  readonly lastSeenAt: string
}

/** 右列的一条活跃授权：谁拿着、还剩多久。 */
export interface HolderTab {
  readonly leaseId: string
  readonly agentId: string
  /** 拿着它的 Agent 的名字；名册里查不到时退回主键。 */
  readonly who: string
  readonly left: string
}

/** 右列：一处受保护资源。 */
export interface DestinationCard {
  readonly id: string
  readonly name: string
  readonly service: string
  readonly kind: string
  readonly isDefault: boolean
  readonly status: string
  /** 现在按什么规则放行；没有学过的规则时为空串。 */
  readonly rule: string
  readonly holders: readonly HolderTab[]
}

export interface WorkbenchInput {
  readonly agents: readonly Agent[]
  readonly identities: readonly Identity[]
  readonly leases: readonly Lease[]
  readonly memories: readonly TrustMemory[]
  readonly now: number
  readonly isHere: boolean
  readonly copy: Copy
}

export function agentCards(input: WorkbenchInput): readonly AgentCard[] {
  const { agents, leases, isHere } = input

  return agents.map((agent) => ({
    id: agent.id,
    name: agent.name,
    kind: agent.type,
    deviceId: agent.device_id,
    isHere,
    trustLevel: agent.trust_level,
    status: agent.status,
    leaseCount: leases.filter((lease) => lease.agentId === agent.id).length,
    lastSeenAt: agent.last_seen_at,
  }))
}

export function destinationCards(input: WorkbenchInput): readonly DestinationCard[] {
  const { agents, identities, leases, memories, now, copy } = input
  const learned = activeMemories(memories)

  return identities.map((identity) => ({
    id: identity.id,
    name: `${identity.service}/${identity.account_label}`,
    service: identity.service,
    kind: identity.environment,
    isDefault: identity.is_default,
    status: identity.status,
    rule: ruleTextOf(learned, identity, copy),
    holders: leases
      .filter((lease) => lease.identityId === identity.id)
      .map((lease) => ({
        leaseId: lease.id,
        agentId: lease.agentId,
        who: nameOf(agents, lease.agentId),
        left: formatRemaining(remainingMillis(lease.expiresAt, now)),
      })),
  }))
}

/**
 * 这处资源现在按什么规则放行。
 *
 * 只数仍然生效的记忆，且**不把它写成一条像规则的句子** —— 记忆是「学过什么」，
 * 展开成什么条件要去 Automation 页面看。一条也没有时返回空串，
 * 由界面说「还没有学过的规则」，而不是在这里编一句「默认拒绝」。
 */
export function ruleTextOf(
  memories: readonly TrustMemory[],
  identity: Identity,
  copy: Copy,
): string {
  const mine = memories.filter((memory) => memory.identity_id === identity.id)
  if (mine.length === 0) {
    return ''
  }
  const ceilings = new Set(mine.map((memory) => memory.risk_ceiling))
  return copy.identitiesRuleSummary(mine.length, [...ceilings].sort().join(' / '))
}

/** 一条 Agent 与一处资源之间还没签署的关系（REQ-IDENT-003 AC1）。 */
export interface Draft {
  readonly agentId: string
  readonly agentName: string
  readonly identityId: string
  readonly identityName: string
}

export function draftOf(agent: AgentCard, destination: DestinationCard): Draft {
  return {
    agentId: agent.id,
    agentName: agent.name,
    identityId: destination.id,
    identityName: destination.name,
  }
}

/** 拖出来的这条关系已经存在了吗 —— 已有活跃 Lease 的组合不必再签一次。 */
export function alreadyHolds(destination: DestinationCard, agentId: string): boolean {
  return destination.holders.some((holder) => holder.agentId === agentId)
}

/** 名册里查不到就退回主键：编一个「未知 Agent」会让「还没拉回来」与「已经不在了」长成一个样子。 */
function nameOf(agents: readonly Agent[], agentId: string): string {
  return agents.find((agent) => agent.id === agentId)?.name ?? agentId
}

/**
 * 草稿去哪里签。
 *
 * 草稿走 URL 而不是存进内存里的 store：它要跨一次路由跳转，而刷新之后
 * 一条只存在于内存里的草稿会消失得无声无息。
 * **不落库**：没有签署的草稿一旦持久化，就和一条真的绑定长得一模一样了。
 */
export function signPath(draft: Draft): string {
  const query = new URLSearchParams({ agent: draft.agentId, identity: draft.identityId })
  return `/automation/advanced/draft?${query.toString()}`
}
