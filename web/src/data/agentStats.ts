import { useQuery } from '@tanstack/react-query'

import { useNow } from './clock'
import { requestGateway, type GatewayRequestOptions } from './gateway'
import { ledgerListSchema, SUMMARY_WINDOW, type LedgerEvent } from './ledgerSummary'

/*
 * Inspector 的「本 Agent 近 7 天」（设计稿 §01）。
 *
 * 与账本摘要同一个来源与同一个限制：账本没有计数端点，数出来的可能是下界，
 * 界面据此显示 `128+` 而不是把下界说成确数。
 */

const SEVEN_DAYS_MS = 7 * 24 * 60 * 60_000

/** 「近 7 天」按小时推进就够了，不必每秒重算。 */
const WINDOW_TICK_MS = 60 * 60_000

export interface AgentStats {
  readonly passed: number
  readonly confirmed: number
  readonly refused: number
  /** 取回的这一窗被这 7 天填满 —— 上面三个数是下界。 */
  readonly isPartial: boolean
}

export const EMPTY_STATS: AgentStats = { passed: 0, confirmed: 0, refused: 0, isPartial: false }

export function agentStatsKey(agentId: string): readonly string[] {
  return ['agent-stats', agentId]
}

export async function fetchAgentEvents(
  agentId: string,
  options: GatewayRequestOptions = {},
): Promise<LedgerEvent[]> {
  const path = `/v1/audit-events?agent_id=${encodeURIComponent(agentId)}&limit=${String(SUMMARY_WINDOW)}`
  return ledgerListSchema.parse(await requestGateway(path, options)).items
}

function isWithinWindow(iso: string, now: number): boolean {
  const at = Date.parse(iso)
  return !Number.isNaN(at) && now - at <= SEVEN_DAYS_MS
}

/**
 * 三个数：通行、需确认、拒绝。
 *
 * 「需确认」数的是走到人这里的那些（`require_approval`），不是「等待中」——
 * 已经被你确认过的也算，它回答的是「这个 Agent 有多常需要你」。
 */
export function summarizeAgent(events: readonly LedgerEvent[], now: number): AgentStats {
  const recent = events.filter((event) => isWithinWindow(event.created_at, now))
  const oldest = events[events.length - 1]

  return {
    passed: recent.filter((event) => event.verdict === 'allow').length,
    confirmed: recent.filter((event) => event.verdict === 'require_approval').length,
    refused: recent.filter((event) => event.verdict === 'deny').length,
    isPartial:
      events.length >= SUMMARY_WINDOW && oldest !== undefined && isWithinWindow(oldest.created_at, now),
  }
}

export interface UseAgentStatsOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (agentId: string, options: GatewayRequestOptions) => Promise<LedgerEvent[]>
}

export function useAgentStats(agentId: string, options: UseAgentStatsOptions = {}): AgentStats {
  const request = options.request ?? fetchAgentEvents
  const now = useNow(WINDOW_TICK_MS)

  const query = useQuery({
    queryKey: agentStatsKey(agentId),
    queryFn: ({ signal }) => request(agentId, { signal }),
    enabled: agentId !== '',
    retry: false,
  })

  return query.data === undefined ? EMPTY_STATS : summarizeAgent(query.data, now)
}
