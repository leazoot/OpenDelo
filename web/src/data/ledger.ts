import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, sendGateway, type GatewayRequestOptions } from './gateway'

/*
 * Boundary Ledger（`GET /v1/audit-events`，REQ-AUDIT-003）。
 *
 * 账本**只在本地追加，不上传**（设计稿 §07）。这里没有一处会把记录送到别处：
 * 导出走的是同一个 Gateway 的导出端点，落成一个本地文件。
 */

export const ledgerRecordSchema = z.object({
  id: z.string().min(1),
  operation_id: z.string(),
  type: z.string(),
  agent_id: z.string(),
  /** 每条都要带设备（REQ-AUDIT-003：一个 Gateway 可能被多台设备审批）。 */
  device_id: z.string(),
  workspace_id: z.string(),
  identity_id: z.string(),
  service: z.string(),
  operation: z.string(),
  resource: z.unknown(),
  resolved_scope: z.unknown(),
  verdict: z.string(),
  risk_level: z.string(),
  lease_id: z.string(),
  lease_status: z.string(),
  outcome: z.string(),
  duration_ms: z.number(),
  is_redacted: z.boolean(),
  created_at: z.string().min(1),
})

export const ledgerPageSchema = z.object({
  items: z.array(ledgerRecordSchema),
  next_cursor: z.string(),
})

export type LedgerRecord = z.infer<typeof ledgerRecordSchema>

/**
 * 一次取多少条。
 *
 * 与后端的分页上限一致。服务端只按 agent / service / before 过滤，
 * 其余维度在这一窗里筛 —— 窗取满时界面要说出「这是这一窗里的结果」，
 * 而不是把它说成账本的全部（AC1 的诚实边界）。
 */
export const LEDGER_WINDOW = 200

/** 服务端认得的过滤条件。agent 与 service 只能二选一（后端如此）。 */
export interface LedgerQuery {
  readonly agentId: string
  readonly service: string
}

export const EMPTY_QUERY: LedgerQuery = { agentId: '', service: '' }

export function ledgerKey(query: LedgerQuery): readonly string[] {
  return ['ledger', query.agentId, query.service]
}

export function ledgerPath(query: LedgerQuery, prefix = '/v1/audit-events'): string {
  const params = new URLSearchParams({ limit: String(LEDGER_WINDOW) })
  if (query.agentId !== '') {
    params.set('agent_id', query.agentId)
  }
  if (query.service !== '') {
    params.set('service', query.service)
  }
  return `${prefix}?${params.toString()}`
}

export async function fetchLedger(
  query: LedgerQuery,
  options: GatewayRequestOptions = {},
): Promise<LedgerRecord[]> {
  return ledgerPageSchema.parse(await requestGateway(ledgerPath(query), options)).items
}

export interface LedgerView {
  readonly records: readonly LedgerRecord[]
  readonly isLoading: boolean
  readonly isError: boolean
  /** 取回的这一窗是满的 —— 页面上看到的是最近的一段，不是全部。 */
  readonly isWindowFull: boolean
}

export interface UseLedgerOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (query: LedgerQuery, options: GatewayRequestOptions) => Promise<LedgerRecord[]>
}

export function useLedger(query: LedgerQuery, options: UseLedgerOptions = {}): LedgerView {
  const request = options.request ?? fetchLedger

  const result = useQuery({
    queryKey: ledgerKey(query),
    queryFn: ({ signal }) => request(query, { signal }),
    retry: false,
  })

  const records = result.data ?? []
  return {
    records,
    isLoading: result.isPending,
    isError: result.isError,
    isWindowFull: records.length >= LEDGER_WINDOW,
  }
}

/** 导出的三种格式（REQ-AUDIT-004）。设计稿的主按钮是 JSONL。 */
export type ExportFormat = 'jsonl' | 'json' | 'csv'

export function exportPath(query: LedgerQuery, format: ExportFormat): string {
  return `${ledgerPath(query, '/v1/audit-events/export')}&format=${format}`
}

/**
 * 导出为本地文件（REQ-AUDIT-004 AC3）。
 *
 * 请求发给同一个 Gateway，响应存成一个 Blob 再由浏览器落盘 ——
 * **全程没有第二个主机**。脱敏在服务端完成，与展示共用同一个视图函数，
 * 因此这里不做任何加工，原样落盘。
 */
export async function downloadExport(
  query: LedgerQuery,
  format: ExportFormat,
  options: GatewayRequestOptions = {},
): Promise<void> {
  const response = await sendGateway(exportPath(query, format), options)
  if (!response.ok) {
    throw new Error(`导出失败：${String(response.status)}`)
  }

  const blob = await response.blob()
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `opendelo-ledger.${format}`
  anchor.click()
  URL.revokeObjectURL(url)
}
