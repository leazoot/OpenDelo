import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * Passage：一次停在缝前或已经穿过缝的请求（REQ-UI-003）。
 *
 * 数据有两个来源，合成同一份列表：
 *   · 打开页面时从 `GET /v1/approvals` 拉一次，得到此刻仍在等人决定的那些；
 *   · 之后由 SSE 的 arrival / passage 事件补上新发生的。
 *
 * 页面打开之前就已经完结的穿越不在这里 —— 那是账本的事（Boundary Ledger）。
 * Gate 回答的是「缝前现在有什么」，不是「今天都发生过什么」。
 */

export type Verdict = 'waiting' | 'allowed' | 'denied' | 'cancelled'

export interface Passage {
  /** 能力请求的主键。事件与列表按它对齐。 */
  readonly id: string
  readonly agentId: string
  readonly service: string
  readonly operation: string
  /** 资源的可读描述，取自请求里的 resource。 */
  readonly resource: string
  readonly verdict: Verdict
  readonly riskLevel: string
  /** 自动允许的理由（REQ-DECIDE-001 AC3）；需要人决定时为空。 */
  readonly reason: string
  /** 等待中的那些带上 approval 主键，供 Folio 与决策使用。 */
  readonly approvalId: string
  /** 这个风险等级下后端允许提供的操作。事件里没有时为空数组。 */
  readonly availableActions: readonly string[]
  readonly at: string
}

const decisionSchema = z.object({
  verdict: z.string(),
  risk_level: z.string(),
  reason_code: z.string(),
})

const requestSchema = z.object({
  id: z.string().min(1),
  agent_id: z.string(),
  service: z.string(),
  operation: z.string(),
  resource: z.unknown(),
  status: z.string(),
  created_at: z.string(),
  decision: decisionSchema.nullable().optional(),
})

const approvalSchema = z.object({
  id: z.string().min(1),
  status: z.string().min(1),
  created_at: z.string(),
  // 这个风险等级下允许提供哪些操作，由后端给出（REQ-APPROVAL-002）。
  // 界面照着渲染，不自己推 —— 「高风险不提供学习类操作」只能有一个答案。
  available_actions: z.array(z.string()).nullable().optional(),
  request: requestSchema.nullable(),
  decision: decisionSchema.nullable(),
})

export const approvalListSchema = z.object({ items: z.array(approvalSchema) })

export type ApprovalPayload = z.infer<typeof approvalSchema>
export type RequestPayload = z.infer<typeof requestSchema>

export const PASSAGES_KEY = ['passages'] as const

/**
 * 列表长度上限。
 *
 * 缝前的画面是「现在」，不是历史。没有上限的话，一个跑了一整天的 Console
 * 会把整天的穿越都堆在这一页上，而它们早就该去账本里找了。
 */
export const MAX_PASSAGES = 40

/** 决策结论 → 缝上的表现。认不出的结论按「等待」处理，不假装它已经有结果。 */
function verdictOf(verdict: string | undefined, status: string): Verdict {
  if (status === 'cancelled') {
    return 'cancelled'
  }
  switch (verdict) {
    case 'allow':
      return 'allowed'
    case 'deny':
      return 'denied'
    default:
      return 'waiting'
  }
}

/** 资源 JSON → 一行可读的描述。 */
export function describeResource(resource: unknown): string {
  if (typeof resource === 'string') {
    return resource
  }
  if (typeof resource !== 'object' || resource === null) {
    return ''
  }
  const parts: string[] = []
  for (const value of Object.values(resource)) {
    if (typeof value === 'string' && value !== '') {
      parts.push(value)
    }
  }
  return parts.join(' · ')
}

export function passageOfApproval(item: ApprovalPayload): Passage | null {
  if (item.request === null) {
    // 请求读不回来的审批项没法在缝上画出来。丢掉一条，好过画一张空卡片。
    return null
  }
  return {
    id: item.request.id,
    agentId: item.request.agent_id,
    service: item.request.service,
    operation: item.request.operation,
    resource: describeResource(item.request.resource),
    verdict: verdictOf(item.decision?.verdict, item.status),
    riskLevel: item.decision?.risk_level ?? '',
    reason: item.decision?.reason_code ?? '',
    approvalId: item.id,
    availableActions: item.available_actions ?? [],
    at: item.created_at,
  }
}

export function passageOfRequest(payload: RequestPayload): Passage {
  return {
    id: payload.id,
    agentId: payload.agent_id,
    service: payload.service,
    operation: payload.operation,
    resource: describeResource(payload.resource),
    verdict: verdictOf(payload.decision?.verdict, payload.status),
    riskLevel: payload.decision?.risk_level ?? '',
    reason: payload.decision?.reason_code ?? '',
    approvalId: '',
    availableActions: [],
    at: payload.created_at,
  }
}

/**
 * 把一条新到的 Passage 并进列表。
 *
 * 按请求主键去重：同一次请求先以 arrival 到达、再以 passage 到达，缝上
 * 始终只有一行，且后到的那份覆盖先到的 —— 结论比「刚到」更新。
 * 已有的那条保持原位，不因为更新而跳到最前面。
 */
export function mergePassage(existing: readonly Passage[], incoming: Passage): Passage[] {
  const at = existing.findIndex((item) => item.id === incoming.id)
  if (at >= 0) {
    const merged = [...existing]
    // approvalId 只在 REST 的审批项里有；事件里没有，不能让它把已有的抹掉。
    const previous = existing[at]
    merged[at] = {
      ...incoming,
      // approvalId 与 availableActions 只在 REST 的审批项里有；事件里没有，
      // 不能让它们把已有的抹掉 —— 抹掉的后果是那一行从此打不开也决定不了。
      approvalId: incoming.approvalId || (previous?.approvalId ?? ''),
      availableActions:
        incoming.availableActions.length > 0 ? incoming.availableActions : (previous?.availableActions ?? []),
    }
    return merged
  }
  return [incoming, ...existing].slice(0, MAX_PASSAGES)
}

export async function fetchPassages(options: GatewayRequestOptions = {}): Promise<Passage[]> {
  const parsed = approvalListSchema.parse(await requestGateway('/v1/approvals', options))
  return parsed.items.map(passageOfApproval).filter((item): item is Passage => item !== null)
}

export interface UsePassagesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Passage[]>
}

export interface PassagesView {
  readonly passages: readonly Passage[]
  readonly isLoading: boolean
  readonly isError: boolean
}

export function usePassages(options: UsePassagesOptions = {}): PassagesView {
  const request = options.request ?? fetchPassages

  const query = useQuery({
    queryKey: PASSAGES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    // 列表由 SSE 保持新鲜；这里不轮询，避免推送与轮询两条路互相覆盖。
    retry: false,
  })

  return {
    passages: query.data ?? [],
    isLoading: query.isPending,
    isError: query.isError,
  }
}

/** 待审批数：等在缝前的那些（顶栏角标与标签标题用它）。 */
export function pendingCountOf(passages: readonly Passage[]): number {
  return passages.filter((item) => item.verdict === 'waiting').length
}
