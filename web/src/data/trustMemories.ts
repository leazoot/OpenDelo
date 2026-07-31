import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * Trust Memory（`GET /v1/trust-memories`，REQ-TRUST-001）。
 *
 * Identities 页面用它回答「这处资源现在按什么规则放行」，Automation 页面
 * 用它列出学过的授权。两处共用一个 query key。
 *
 * 只取展示需要的字段：记忆里没有凭据，这里也不该出现能装下它的位置。
 */

export const trustMemorySchema = z.object({
  id: z.string().min(1),
  agent_id: z.string(),
  workspace_id: z.string(),
  identity_id: z.string(),
  service: z.string(),
  environment: z.string(),
  risk_ceiling: z.string(),
  approval_behavior: z.string(),
  /** 产生这条记忆的那次审批（REQ-TRUST-001 AC2）。 */
  created_from: z.string(),
  status: z.string(),
  invalidation_reason: z.string(),
  expires_at: z.string(),
  created_at: z.string(),
})

export const trustMemoryListSchema = z.object({ items: z.array(trustMemorySchema) })

export type TrustMemory = z.infer<typeof trustMemorySchema>

export const TRUST_MEMORIES_KEY = ['trust-memories'] as const

export async function fetchTrustMemories(
  options: GatewayRequestOptions = {},
): Promise<TrustMemory[]> {
  return trustMemoryListSchema.parse(await requestGateway('/v1/trust-memories', options)).items
}

export interface TrustMemoriesView {
  readonly memories: readonly TrustMemory[]
  readonly isLoading: boolean
  readonly isError: boolean
}

export interface UseTrustMemoriesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<TrustMemory[]>
}

export function useTrustMemories(options: UseTrustMemoriesOptions = {}): TrustMemoriesView {
  const request = options.request ?? fetchTrustMemories

  const query = useQuery({
    queryKey: TRUST_MEMORIES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return { memories: query.data ?? [], isLoading: query.isPending, isError: query.isError }
}

/** 仍在生效的那些。失效的记忆不消失，但它不再解释「现在按什么规则放行」。 */
export function activeMemories(memories: readonly TrustMemory[]): readonly TrustMemory[] {
  return memories.filter((memory) => memory.status === 'active')
}

export interface TightenView {
  readonly tighten: (id: string) => void
  readonly isPending: boolean
  readonly isDone: boolean
  readonly isError: boolean
}

export interface UseTightenOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly tighten?: (id: string) => Promise<void>
}

/**
 * 把一条记忆改成「每次都问」（REQ-TRUST-005）。
 *
 * 请求体里只有一个取值，因为端点只接受这一个：**改回自动允许是放宽**，
 * 而放宽只能由一次新的审批产生。前端这里连表达它的参数都没有。
 */
export async function tightenMemory(id: string, options: GatewayRequestOptions = {}): Promise<void> {
  await requestGateway(`/v1/trust-memories/${encodeURIComponent(id)}`, {
    ...options,
    method: 'PATCH',
    body: { approval_behavior: 'always_ask' },
  })
}

export function useTightenMemory(options: UseTightenOptions = {}): TightenView {
  const client = useQueryClient()
  const send = options.tighten ?? ((id: string) => tightenMemory(id))
  const mutation = useMutation({
    mutationFn: send,
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: TRUST_MEMORIES_KEY })
    },
  })

  return {
    tighten: (id: string) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(id)
    },
    isPending: mutation.isPending,
    isDone: mutation.isSuccess,
    isError: mutation.isError,
  }
}
