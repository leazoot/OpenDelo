import { useQuery } from '@tanstack/react-query'

import { fetchAgentEvents } from '../../data/agentStats'
import type { GatewayRequestOptions } from '../../data/gateway'
import { SUMMARY_WINDOW, type LedgerEvent } from '../../data/ledgerSummary'
import type { TrustMemory } from '../../data/trustMemories'

import { impactOf, manuscriptOf, type Impact } from './manuscript'

/*
 * 影响预览的数据来源（REQ-TRUST-006 AC2）。
 *
 * 走的是账本自己的端点，与 Inspector 的「近 7 天」同一条路 ——
 * 三个数字必须来自真实历史，占位值会让「签下去会发生什么」变成一句空话。
 */

const EMPTY: Impact = { confirmed: 0, passed: 0, refused: 0, isPartial: false }

export interface UseImpactOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (agentId: string, options: GatewayRequestOptions) => Promise<LedgerEvent[]>
}

export function useManuscriptImpact(
  agentId: string,
  memory: TrustMemory | null,
  now: number,
  options: UseImpactOptions = {},
): Impact {
  const request = options.request ?? fetchAgentEvents

  const query = useQuery({
    queryKey: ['manuscript-impact', agentId],
    queryFn: ({ signal }) => request(agentId, { signal }),
    enabled: agentId !== '',
    retry: false,
  })

  if (query.data === undefined || memory === null) {
    return EMPTY
  }
  return impactOf(query.data, manuscriptOf(memory), now, SUMMARY_WINDOW)
}
