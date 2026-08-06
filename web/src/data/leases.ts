import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'
import { LEASES_KEY } from './liveUpdates'

/*
 * 生效中的 Lease（REQ-LEASE-003）。
 *
 * 缝内侧那一排标签。每条显示名称、Scope 与剩余时间，剩余时间按秒走
 * —— 授权是有期限的，看不见它在走就等于没有期限。
 */

export const leaseSchema = z.object({
  id: z.string().min(1),
  agent_id: z.string(),
  identity_id: z.string(),
  service: z.string(),
  resource_scope: z.unknown(),
  expires_at: z.string().min(1),
  status: z.string().min(1),
  is_session_bound: z.boolean(),
})

export const leaseListSchema = z.object({ items: z.array(leaseSchema) })

export type LeasePayload = z.infer<typeof leaseSchema>

export interface Lease {
  readonly id: string
  readonly agentId: string
  readonly identityId: string
  readonly service: string
  /** Scope 的可读描述，取自 resource_scope。 */
  readonly scope: string
  readonly expiresAt: string
  readonly isSessionBound: boolean
}

/** 剩余时间小于这个值时改用 --wait 色（REQ-LEASE-003 AC3）。 */
export const ENDING_SOON_MS = 60_000

export function leaseOf(payload: LeasePayload): Lease {
  return {
    id: payload.id,
    agentId: payload.agent_id,
    identityId: payload.identity_id,
    service: payload.service,
    scope: describeScope(payload.resource_scope),
    expiresAt: payload.expires_at,
    isSessionBound: payload.is_session_bound,
  }
}

/**
 * Scope JSON → 缝内侧那一排小标签上的一行字。
 *
 * 只说**这条授权覆盖了什么**：资源与操作。收敛出来的 Scope 有十一个维度，
 * 把它们全铺开会得到一行读不懂的长串（两个 ULID、身份 ID、两个时间戳……），
 * 而标签只有一行的位置。谁、什么时候、什么环境在 Inspector 与卷宗里回答。
 */
export function describeScope(scope: unknown): string {
  if (typeof scope === 'string') {
    return scope
  }
  if (typeof scope !== 'object' || scope === null) {
    return ''
  }

  // 用 `in` 收窄而不是断言：断言会让「服务端改了字段名」这件事在类型上消失，
  // 而它的表现是标签默默变空（项目 lint 也禁止 as）。
  const resource = 'resource' in scope ? scope.resource : undefined
  const operation = 'operation' in scope ? scope.operation : undefined

  const parts: string[] = []
  if (typeof resource === 'object' && resource !== null) {
    for (const value of Object.values(resource)) {
      if (typeof value === 'string' && value !== '') {
        parts.push(value)
      }
    }
  }
  if (typeof operation === 'string' && operation !== '') {
    parts.push(operation)
  }
  return parts.join(' · ')
}

/**
 * 还剩多少毫秒。已经过期或时刻认不出时返回 0。
 *
 * 认不出的时刻按「已到期」处理而不是按「还早」：这一侧的错误只会让界面
 * 少显示一条授权，反过来会让一条早已失效的授权看起来还在生效。
 */
export function remainingMillis(expiresAt: string, now: number): number {
  const deadline = Date.parse(expiresAt)
  if (Number.isNaN(deadline)) {
    return 0
  }
  return Math.max(0, deadline - now)
}

/**
 * 剩余时间的写法：超过一分钟只说分钟，一分钟以内说到秒。
 *
 * 分钟向上取整：显示「1m」的那一刻真实剩余在 1 分 0 秒到 1 分 59 秒之间，
 * 误差不超过一分钟且永远不会把还剩 59 秒说成 0m（REQ-LEASE-003 AC1）。
 */
export function formatRemaining(millis: number): string {
  if (millis <= 0) {
    return '0s'
  }
  const seconds = Math.ceil(millis / 1_000)
  if (millis < ENDING_SOON_MS) {
    return `${String(seconds)}s`
  }
  return `${String(Math.ceil(seconds / 60))}m`
}

export function isEndingSoon(millis: number): boolean {
  return millis < ENDING_SOON_MS
}

export async function fetchLeases(options: GatewayRequestOptions = {}): Promise<Lease[]> {
  const parsed = leaseListSchema.parse(await requestGateway('/v1/leases', options))
  return parsed.items.map(leaseOf)
}

export interface UseLeasesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Lease[]>
}

export interface LeasesView {
  readonly leases: readonly Lease[]
  readonly isLoading: boolean
  readonly isError: boolean
}

export function useLeases(options: UseLeasesOptions = {}): LeasesView {
  const request = options.request ?? fetchLeases

  const query = useQuery({
    queryKey: LEASES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return { leases: query.data ?? [], isLoading: query.isPending, isError: query.isError }
}

export async function revokeLease(id: string, options: GatewayRequestOptions = {}): Promise<void> {
  await requestGateway(`/v1/leases/${encodeURIComponent(id)}`, { ...options, method: 'DELETE' })
}

export interface UseRevokeLeaseOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly revoke?: (id: string) => Promise<void>
}

export interface RevokeLeaseView {
  readonly revoke: (id: string) => void
  /** 正在提交的那条；重复点击由它拦住（REQ-APPROVAL-006 AC2 的同一条理由）。 */
  readonly pendingId: string
  readonly isError: boolean
}

/**
 * 收回一条 Lease。
 *
 * 乐观更新：那一条立刻从缝内侧消失，失败则放回去并把错误留给调用方。
 * 不静默吞掉失败 —— 用户以为收回了、
 * 而授权还活着，是这个产品里最不该发生的一种错觉。
 */
export function useRevokeLease(options: UseRevokeLeaseOptions = {}): RevokeLeaseView {
  const client = useQueryClient()
  const send = options.revoke ?? ((id: string) => revokeLease(id))

  const mutation = useMutation({
    mutationFn: send,
    onMutate: async (id: string) => {
      await client.cancelQueries({ queryKey: LEASES_KEY })
      const previous = client.getQueryData<Lease[]>(LEASES_KEY)
      client.setQueryData<Lease[]>(LEASES_KEY, (existing) =>
        (existing ?? []).filter((item) => item.id !== id),
      )
      return { previous }
    },
    onError: (_error, _id, context) => {
      if (context?.previous !== undefined) {
        client.setQueryData<Lease[]>(LEASES_KEY, context.previous)
      }
    },
    onSettled: () => {
      // 服务端也会推一条 lease 事件；这里再失效一次，别的窗口靠推送、
      // 这个窗口靠这一次，两条路都不依赖对方（REQ-LEASE-003 的 5 秒同步）。
      void client.invalidateQueries({ queryKey: LEASES_KEY })
    },
  })

  return {
    revoke: (id: string) => {
      mutation.mutate(id)
    },
    pendingId: mutation.isPending ? mutation.variables : '',
    isError: mutation.isError,
  }
}
