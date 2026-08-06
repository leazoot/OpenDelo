import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * Agent 的名字（`GET /v1/agents`，REQ-API-002）。
 *
 * 能力请求里只有 agent_id。缝上要显示的是「谁在敲门」，而一串 ULID 回答不了这个。
 */

export const agentSchema = z.object({
  id: z.string().min(1),
  name: z.string(),
  type: z.string(),
  /** 请求实际来自哪台设备（REQ-UI-005 AC2）。 */
  device_id: z.string(),
  workspace_id: z.string(),
  trust_level: z.string(),
  status: z.string(),
  last_seen_at: z.string(),
})

export const agentListSchema = z.object({ items: z.array(agentSchema) })

export type Agent = z.infer<typeof agentSchema>

export const AGENTS_KEY = ['agents'] as const

export async function fetchAgents(options: GatewayRequestOptions = {}): Promise<Agent[]> {
  return agentListSchema.parse(await requestGateway('/v1/agents', options)).items
}

export interface AgentsView {
  readonly agents: readonly Agent[]
  readonly isLoading: boolean
  readonly isError: boolean
}

export interface UseAgentsOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Agent[]>
}

/**
 * 整份 Agent 名册（Identities 页面的左列）。
 *
 * 与 useAgentNames 共用一个 query key：两处要的是同一份数据，
 * 拉两次只会让两列在不同时刻各自过时。
 */
export function useAgents(options: UseAgentsOptions = {}): AgentsView {
  const request = options.request ?? fetchAgents

  const query = useQuery({
    queryKey: AGENTS_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return { agents: query.data ?? [], isLoading: query.isPending, isError: query.isError }
}

export interface UseAgentNamesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Agent[]>
}

/**
 * agent_id → 名字。
 *
 * 查不到名字时调用方拿到的是 undefined，由它决定退回显示什么 ——
 * 在这里编一个「未知 Agent」会让「名字还没拉回来」和「这个 Agent 已经不在了」
 * 长成同一个样子。
 */
export function useAgentNames(options: UseAgentNamesOptions = {}): ReadonlyMap<string, string> {
  const request = options.request ?? fetchAgents

  const query = useQuery({
    queryKey: AGENTS_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return new Map((query.data ?? []).map((agent) => [agent.id, agent.name]))
}

/**
 * 卡片左上角的两字母标记。
 *
 * 取名字里各段的首字母（`writer-agent` → WA）。名字为空时退回主键的前两位 ——
 * 那仍然能把两行不同的请求区分开，而空着的方块只是一个洞。
 */
export function markOf(name: string, fallback: string): string {
  const initials = name
    .split(/[-_\s.]+/)
    .filter((part) => part !== '')
    .map((part) => part[0] ?? '')
    .join('')
  const chosen = initials === '' ? fallback : initials
  return chosen.slice(0, 2).toUpperCase()
}

/*
 * 确认一个 Agent 是你自己启动的（REQ-AGENT-002 AC3）。
 *
 * 未确认的 Agent 写操作永远不会被自动放行 —— 那道门与风险等级无关，学习也打不开它。
 * 因此「今后在当前项目自动允许」要真的生效，先得有人在这里点一下头。
 *
 * 只往上升，不提供降级：PRD 没有要求撤回确认，而多一个能把 `known` 打回
 * `unverified` 的入口，等于多一条能悄悄改变风险输入的路。
 */

/** TRUST_UNVERIFIED 是新注册 Agent 的等级（REQ-AGENT-002 AC1）。 */
export const TRUST_UNVERIFIED = 'unverified'

export async function confirmAgent(id: string, options: GatewayRequestOptions = {}): Promise<void> {
  await requestGateway(`/v1/agents/${encodeURIComponent(id)}/trust`, {
    ...options,
    method: 'POST',
    body: { confirmed: true },
  })
}

export interface UseConfirmAgentOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly confirm?: (id: string) => Promise<void>
}

export interface ConfirmAgentView {
  confirm: (id: string) => void
  /** 正在确认的那个 Agent；没有时为空串。重复提交由它拦住。 */
  readonly pendingId: string
  readonly isError: boolean
}

export function useConfirmAgent(options: UseConfirmAgentOptions = {}): ConfirmAgentView {
  const client = useQueryClient()
  const send = options.confirm ?? ((id: string) => confirmAgent(id))

  const mutation = useMutation({
    mutationFn: send,
    // 不做乐观更新：信任等级是风险引擎的输入，先显示成已确认再回滚，
    // 中间那一瞬间界面说的是一件还没成立的事。
    onSettled: () => {
      void client.invalidateQueries({ queryKey: AGENTS_KEY })
    },
  })

  return {
    confirm: (id: string) => {
      mutation.mutate(id)
    },
    pendingId: mutation.isPending ? mutation.variables : '',
    isError: mutation.isError,
  }
}
