import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { useNow } from './clock'
import { requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * 账本摘要（设计稿 §01 底部的 Boundary ledger 条）。
 *
 * Gate 上只给「今天过了多少、拦了多少」和最近几条。完整的账本是 Boundary Ledger
 * 那一页的事，这里不做过滤器也不做图表。
 */

export const ledgerEventSchema = z.object({
  id: z.string().min(1),
  type: z.string(),
  verdict: z.string(),
  outcome: z.string(),
  service: z.string(),
  agent_id: z.string(),
  created_at: z.string().min(1),
})

export const ledgerListSchema = z.object({ items: z.array(ledgerEventSchema) })

export type LedgerEvent = z.infer<typeof ledgerEventSchema>

export const LEDGER_SUMMARY_KEY = ['ledger', 'summary'] as const

/**
 * 一次取多少条。
 *
 * 账本没有计数端点，今天的次数只能从取回的这一窗里数。窗取满时数出来的是下界，
 * 界面据此显示 `34+` 而不是把下界说成确数（见 LedgerSummary.isPartial）。
 */
export const SUMMARY_WINDOW = 200

/** 条带上摆几条。设计稿是四列。 */
export const RIBBON_SIZE = 4

export interface LedgerSummary {
  readonly passed: number
  readonly refused: number
  /** 取回的这一窗被取满且最老一条仍是今天 —— 上面两个数是下界。 */
  readonly isPartial: boolean
  readonly recent: readonly LedgerEvent[]
}

export async function fetchLedgerEvents(options: GatewayRequestOptions = {}): Promise<LedgerEvent[]> {
  const path = `/v1/audit-events?limit=${String(SUMMARY_WINDOW)}`
  return ledgerListSchema.parse(await requestGateway(path, options)).items
}

/** 同一天。按本地时区判断 —— 用户说的「今天」是他所在时区的今天。 */
export function isSameDay(iso: string, now: number): boolean {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return false
  }
  const today = new Date(now)
  return (
    at.getFullYear() === today.getFullYear() &&
    at.getMonth() === today.getMonth() &&
    at.getDate() === today.getDate()
  )
}

export function summarize(events: readonly LedgerEvent[], now: number): LedgerSummary {
  const today = events.filter((event) => isSameDay(event.created_at, now))
  const oldest = events[events.length - 1]

  return {
    passed: today.filter((event) => event.verdict === 'allow').length,
    refused: today.filter((event) => event.verdict === 'deny').length,
    isPartial: events.length >= SUMMARY_WINDOW && oldest !== undefined && isSameDay(oldest.created_at, now),
    recent: events.slice(0, RIBBON_SIZE),
  }
}

export interface UseLedgerSummaryOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<LedgerEvent[]>
}

export interface LedgerSummaryView {
  readonly summary: LedgerSummary
  readonly isLoading: boolean
  readonly isError: boolean
}

const EMPTY: LedgerSummary = { passed: 0, refused: 0, isPartial: false, recent: [] }

/** 「今天」只需要按分钟推进：跨过午夜时摘要最多晚一分钟归零。 */
const DAY_TICK_MS = 60_000

export function useLedgerSummary(options: UseLedgerSummaryOptions = {}): LedgerSummaryView {
  const request = options.request ?? fetchLedgerEvents
  const now = useNow(DAY_TICK_MS)

  const query = useQuery({
    queryKey: LEDGER_SUMMARY_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return {
    summary: query.data === undefined ? EMPTY : summarize(query.data, now),
    isLoading: query.isPending,
    isError: query.isError,
  }
}
