import { describe, expect, it } from 'vitest'

import {
  connectionStateOf,
  GATEWAY_POLL_INTERVAL_MS,
  fetchGatewayStatus,
  gatewayStatusSchema,
  type GatewayStatus,
} from './gatewayStatus'

const RUNNING: GatewayStatus = {
  status: 'running',
  version: '1.2.3-test',
  listen_address: '127.0.0.1',
  web_api_port: 8787,
  started_at: '2026-07-28T09:15:30.123Z',
}

function jsonFetch(status: number, body: unknown): typeof fetch {
  return () =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
      }),
    )
}

describe('gatewayStatusSchema', () => {
  it('解析完整的状态响应', () => {
    expect(gatewayStatusSchema.parse(RUNNING)).toEqual(RUNNING)
  })

  it.each(['status', 'version', 'listen_address', 'web_api_port', 'started_at'])(
    '缺 %s 时拒绝，而不是留下一个 undefined 等着渲染时炸',
    (field) => {
      const incomplete = Object.fromEntries(Object.entries(RUNNING).filter(([key]) => key !== field))

      expect(() => gatewayStatusSchema.parse(incomplete)).toThrow()
    },
  )

  it('字段类型不对时拒绝', () => {
    expect(() => gatewayStatusSchema.parse({ ...RUNNING, web_api_port: '8787' })).toThrow()
  })

  it('端口不是正整数时拒绝', () => {
    expect(() => gatewayStatusSchema.parse({ ...RUNNING, web_api_port: 0 })).toThrow()
  })
})

describe('fetchGatewayStatus', () => {
  it('返回解析后的状态', async () => {
    const status = await fetchGatewayStatus({ token: 'test-token', fetchImpl: jsonFetch(200, RUNNING) })

    expect(status).toEqual(RUNNING)
  })

  it('响应结构不符合契约时失败，不返回半个对象', async () => {
    await expect(
      fetchGatewayStatus({ token: 'test-token', fetchImpl: jsonFetch(200, { status: 'running' }) }),
    ).rejects.toThrow()
  })
})

describe('connectionStateOf', () => {
  it('还没有结果时是连接中', () => {
    expect(connectionStateOf(false, null)).toBe('connecting')
  })

  it('探测失败时是未连接', () => {
    expect(connectionStateOf(true, null)).toBe('disconnected')
  })

  it('探测失败时不因为还留着上一次的数据而显示已连接', () => {
    // 缓存里的旧状态描述的是过去，不是现在。
    expect(connectionStateOf(true, RUNNING)).toBe('disconnected')
  })

  it('Gateway 自报 running 时是已连接', () => {
    expect(connectionStateOf(false, RUNNING)).toBe('connected')
  })

  it('认不出的状态一律按未连接处理', () => {
    // 端口上有东西在应答不等于 Gateway 在跑。
    expect(connectionStateOf(false, { ...RUNNING, status: 'starting' })).toBe('disconnected')
  })
})

describe('轮询间隔', () => {
  it('留在 REQ-GATEWAY-002 AC1 的 5 秒预算之内', () => {
    // 一次探测的耗时加上这个间隔必须小于 5 秒，否则「断开后 5 秒内变为
    // disconnected」这句话在最坏情况下就不成立。
    expect(GATEWAY_POLL_INTERVAL_MS).toBeLessThan(5_000)
  })
})
