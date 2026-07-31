import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

/**
 * GET /v1/gateway/status 的响应（REQ-API-002）。
 *
 * 用 schema 解析而不是类型断言：断言只让类型检查闭嘴，字段真的缺了要到渲染时才炸
 */
export const gatewayStatusSchema = z.object({
  status: z.string().min(1),
  version: z.string().min(1),
  listen_address: z.string().min(1),
  web_api_port: z.number().int().positive(),
  started_at: z.string().min(1),
})

export type GatewayStatus = z.infer<typeof gatewayStatusSchema>

/**
 * Gateway 的连接状态（REQ-GATEWAY-002）。
 *
 * 没有 `locked`：保险库锁着是缝内的事，不影响 Console 连不连得上 Gateway。
 */
export type ConnectionState = 'connecting' | 'connected' | 'disconnected'

/** Gateway 自报的运行状态里唯一表示「在跑」的取值。 */
const RUNNING = 'running'

/**
 * 轮询间隔。REQ-GATEWAY-002 AC1 要求断开后 5 秒内顶栏变为 disconnected：
 * 一次探测的耗时加上这个间隔必须留在 5 秒以内。
 */
export const GATEWAY_POLL_INTERVAL_MS = 3_000

export const GATEWAY_STATUS_KEY = ['gateway', 'status'] as const

export async function fetchGatewayStatus(options: GatewayRequestOptions = {}): Promise<GatewayStatus> {
  return gatewayStatusSchema.parse(await requestGateway('/v1/gateway/status', options))
}

export interface GatewayStatusView {
  readonly state: ConnectionState
  /** 最近一次成功读到的状态。从未成功过时为 null。 */
  readonly status: GatewayStatus | null
}

export interface UseGatewayStatusOptions {
  /** 覆盖轮询间隔，只在测试里用。 */
  readonly intervalMs?: number
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<GatewayStatus>
}

export function useGatewayStatus(options: UseGatewayStatusOptions = {}): GatewayStatusView {
  const request = options.request ?? fetchGatewayStatus

  const query = useQuery({
    queryKey: GATEWAY_STATUS_KEY,
    queryFn: ({ signal }) => request({ signal }),
    refetchInterval: options.intervalMs ?? GATEWAY_POLL_INTERVAL_MS,
    // 不重试：重试只会推迟「已经连不上了」这个结论，而那正是要尽快说出口的事。
    retry: false,
  })

  return {
    state: connectionStateOf(query.isError, query.data ?? null),
    status: query.data ?? null,
  }
}

/**
 * 由查询结果推导连接状态。
 *
 * 认不出的 status 一律按未连接处理：端口上有东西在应答不等于 Gateway 在跑，
 * 把它显示成「已连接」会让这句话变得不可信。
 */
export function connectionStateOf(failed: boolean, status: GatewayStatus | null): ConnectionState {
  if (failed) {
    return 'disconnected'
  }
  if (status === null) {
    return 'connecting'
  }
  return status.status === RUNNING ? 'connected' : 'disconnected'
}
