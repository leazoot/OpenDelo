import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'
import { describeResource } from './passages'

/*
 * Access Folio 的内容（`GET /v1/capability-requests/:id`，REQ-APPROVAL-001）。
 *
 * 列表给的是缝上画一行所需的最少内容；卷宗要回答 PRD §13.1 的十一个问题，
 * 因此走请求详情这条路，把决策的风险因子与解析后的 Scope 一起取回来。
 *
 * 这里同样没有任何字段能表达凭据 —— 响应里就没有（见 httpapi/views.go）。
 */

const resolvedScopeSchema = z
  .object({
    agent_id: z.string(),
    workspace_id: z.string(),
    service: z.string(),
    identity_id: z.string(),
    account: z.string(),
    resource: z.record(z.string()).nullable(),
    resource_key: z.string(),
    operation: z.string(),
    not_before: z.string(),
    expires_at: z.string(),
    request_limit: z.number(),
    environment: z.string(),
    risk_ceiling: z.string(),
  })
  .partial()

const folioDecisionSchema = z.object({
  verdict: z.string(),
  risk_level: z.string(),
  // 触发该等级的因子（REQ-RISK-001 AC3）。没有它，风险等级就只是一个
  // 无从解释的标签，而审批页要回答的恰恰是「为什么是这个等级」。
  risk_factors: z.array(z.string()).nullable(),
  identity_id: z.string(),
  reason_code: z.string(),
  resolved_scope: resolvedScopeSchema.nullable(),
})

/*
 * 查勘结果：执行前从外部服务读回来的旧值（REQ-APPROVAL-001 AC4）。
 *
 * before 与 after 在后端是 omitempty，缺席表示「这一侧没有值」——
 * 新增一个字段时 before 缺席，删除时 after 缺席。补成空串是对的：
 * 「没有这一项」与「值是空的」在展示上都是空，但这里不需要区分。
 */
const changePreviewSchema = z.array(
  z.object({
    resource: z.string().default(''),
    field: z.string(),
    before: z.string().default(''),
    after: z.string().default(''),
  }),
)

export const folioRequestSchema = z.object({
  id: z.string().min(1),
  agent_id: z.string(),
  workspace_id: z.string(),
  service: z.string(),
  operation: z.string(),
  resource: z.unknown(),
  desired_change: z.unknown(),
  // null 表示**没有查过**，不是「查过但没有可对照的字段」——
  // 卷宗对这两种情况说的话不一样。
  change_preview: changePreviewSchema.nullable(),
  reason: z.string(),
  status: z.string().min(1),
  // 这个服务里没有被这次请求覆盖的操作，来自 Adapter 的能力表。
  withheld_operations: z.array(z.string()).nullable(),
  created_at: z.string(),
  decision: folioDecisionSchema.nullable(),
})

export type FolioPayload = z.infer<typeof folioRequestSchema>
export type ResolvedScope = z.infer<typeof resolvedScopeSchema>

/** 期望变更里的一项：字段、它将被写成的值，以及查勘查到的旧值。 */
export interface ChangeField {
  readonly field: string
  readonly value: string
  /** 查勘查到的当前值。查勘没做过或这一项原本不存在时为空串。 */
  readonly before: string
}

/** 回滚能力。由 Adapter 的声明经风险因子传过来，不是猜的。 */
export type Rollback = 'not-applicable' | 'none' | 'possible'

export interface Folio {
  readonly id: string
  readonly agentId: string
  readonly workspaceId: string
  readonly service: string
  readonly operation: string
  readonly resource: string
  /** 请求要写入的内容。读操作为空数组。 */
  readonly change: readonly ChangeField[]
  /** 执行前查过一次旧值。为假时卷宗照实说「当前值尚未查询」，不编一个出来。 */
  readonly isPreviewed: boolean
  /** 这个服务里仍然关闭的操作。空数组表示能力表回答不了这个问题。 */
  readonly withheldOperations: readonly string[]
  /** Agent 自述的理由。 */
  readonly reason: string
  readonly status: string
  readonly identityId: string
  readonly riskLevel: string
  readonly riskFactors: readonly string[]
  /** 决策给出的理由码，与 Inspector 的 Rule 一行同源。 */
  readonly reasonCode: string
  readonly scope: ResolvedScope
  readonly rollback: Rollback
  readonly createdAt: string
}

/**
 * 已经有结论、不再等人的状态。
 *
 * 写成一个封闭集合而不是「不等于 awaiting_approval」：认不出的状态因此
 * **不会**被说成「已处理」—— 那句提示一旦说错，用户会以为自己晚了一步而离开。
 */
const SETTLED_STATUSES: ReadonlySet<string> = new Set([
  'auto_allowed',
  'denied',
  'approved',
  'rejected',
  'expired',
  'cancelled',
  'executing',
  'succeeded',
  'failed',
])

/** 这条请求已经有结论了（REQ-APPROVAL-004 AC3 的提示由它触发）。 */
export function isSettled(status: string): boolean {
  return SETTLED_STATUSES.has(status)
}

/**
 * 这条请求还能被决定。
 *
 * 与 isSettled 分开写，两者各自向安全的一侧失败：认不出的状态既不显示
 * 「已处理」，也不允许决定。合成一个布尔量就必然有一侧要说谎。
 */
export function isDecidable(status: string): boolean {
  return status === 'awaiting_approval'
}

/**
 * 期望变更 JSON → 逐项的字段、新值与旧值。读操作（null）得到空数组。
 *
 * 旧值按字段名对上查勘结果。对不上的字段旧值为空串 —— 查勘只查了对照字段
 * 白名单里的那几项，别的字段本来就没有旧值可说。
 */
export function changeOf(desired: unknown, preview: ChangePreview | null = null): ChangeField[] {
  if (typeof desired !== 'object' || desired === null) {
    return []
  }
  const before = new Map((preview ?? []).map((change) => [change.field, change.before]))
  return Object.entries(desired).map(([field, value]) => ({
    field,
    value: typeof value === 'string' ? value : JSON.stringify(value),
    before: before.get(field) ?? '',
  }))
}

export type ChangePreview = z.infer<typeof changePreviewSchema>

/**
 * 能不能还原。
 *
 * `irreversible` 因子的含义是「写操作且 Adapter 声明不可逆」，因此它在场就是
 * 不可回滚；不在场且这次确实要写，说明 Adapter 声明了可逆。两者都来自声明，
 * 不是从操作名上猜出来的（REQ-INTENT-002）。
 */
export function rollbackOf(factors: readonly string[], hasChange: boolean): Rollback {
  if (factors.includes('irreversible')) {
    return 'none'
  }
  if (!hasChange) {
    return 'not-applicable'
  }
  return 'possible'
}

export function folioOf(payload: FolioPayload): Folio {
  const factors = payload.decision?.risk_factors ?? []
  const change = changeOf(payload.desired_change, payload.change_preview)

  return {
    id: payload.id,
    agentId: payload.agent_id,
    workspaceId: payload.workspace_id,
    service: payload.service,
    operation: payload.operation,
    resource: describeResource(payload.resource),
    change,
    isPreviewed: payload.change_preview !== null,
    withheldOperations: payload.withheld_operations ?? [],
    reason: payload.reason,
    status: payload.status,
    identityId: payload.decision?.identity_id ?? '',
    riskLevel: payload.decision?.risk_level ?? '',
    riskFactors: factors,
    reasonCode: payload.decision?.reason_code ?? '',
    scope: payload.decision?.resolved_scope ?? {},
    rollback: rollbackOf(factors, change.length > 0),
    createdAt: payload.created_at,
  }
}

export function folioKey(id: string): readonly string[] {
  return ['folio', id]
}

export async function fetchFolio(id: string, options: GatewayRequestOptions = {}): Promise<Folio> {
  const path = `/v1/capability-requests/${encodeURIComponent(id)}`
  return folioOf(folioRequestSchema.parse(await requestGateway(path, options)))
}

export interface UseFolioOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (id: string, options: GatewayRequestOptions) => Promise<Folio>
}

export interface FolioView {
  readonly folio: Folio | null
  readonly isLoading: boolean
  readonly isError: boolean
  /** 这条请求根本不在（404）。与「读不到」是两句不同的话。 */
  readonly isMissing: boolean
}

export function useFolio(id: string, options: UseFolioOptions = {}): FolioView {
  const request = options.request ?? fetchFolio

  const query = useQuery({
    queryKey: folioKey(id),
    queryFn: ({ signal }) => request(id, { signal }),
    enabled: id !== '',
    retry: false,
  })

  return {
    folio: query.data ?? null,
    isLoading: query.isPending && id !== '',
    isError: query.isError,
    isMissing: statusOfError(query.error) === 404,
  }
}

function statusOfError(error: unknown): number {
  return typeof error === 'object' && error !== null && 'status' in error && typeof error.status === 'number'
    ? error.status
    : 0
}
