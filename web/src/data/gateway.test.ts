import { describe, expect, it, vi } from 'vitest'

import { GatewayError, requestGateway } from './gateway'

const TOKEN = 'test-session-token-4c1f0a9e'

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') {
    return input
  }
  return input instanceof URL ? input.href : input.url
}

/** 记录收到的请求，并按调用方给定的方式应答。 */
function fakeFetch(response: Response): { send: typeof fetch; calls: { url: string; init?: RequestInit }[] } {
  const calls: { url: string; init?: RequestInit }[] = []
  const send = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(init === undefined ? { url: urlOf(input) } : { url: urlOf(input), init })
    return Promise.resolve(response)
  })
  return { send, calls }
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}

function headersOf(init: RequestInit | undefined): Headers {
  return new Headers(init?.headers)
}

describe('requestGateway', () => {
  it('把会话令牌与自定义头放在请求头里，不放进 URL', async () => {
    const { send, calls } = fakeFetch(jsonResponse(200, { status: 'running' }))

    await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send })

    expect(calls).toHaveLength(1)
    const [call] = calls
    expect(call?.url).toBe('/v1/gateway/status')
    expect(call?.url).not.toContain(TOKEN)

    const headers = headersOf(call?.init)
    expect(headers.get('Authorization')).toBe(`Bearer ${TOKEN}`)
    expect(headers.get('X-Requested-By')).toBe('opendelo-console')
  })

  it('不带 Cookie：认证只有令牌一条路径', async () => {
    const { send, calls } = fakeFetch(jsonResponse(200, {}))

    await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send })

    expect(calls[0]?.init?.credentials).toBe('omit')
  })

  it('返回解析后的响应体', async () => {
    const { send } = fakeFetch(jsonResponse(200, { status: 'running', version: '1.2.3' }))

    const body = await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send })

    expect(body).toEqual({ status: 'running', version: '1.2.3' })
  })

  it('把 401 的错误契约还原成 GatewayError', async () => {
    const { send } = fakeFetch(
      jsonResponse(401, {
        error: { code: 'unauthenticated', message: '缺少有效的身份凭证。', operation_id: '01K1AAAA' },
      }),
    )

    const failure = await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send }).catch(
      (error: unknown) => error,
    )

    expect(failure).toBeInstanceOf(GatewayError)
    expect(failure).toMatchObject({
      status: 401,
      code: 'unauthenticated',
      message: '缺少有效的身份凭证。',
      operationId: '01K1AAAA',
    })
  })

  it('响应结构不符合错误契约时不假装读懂了它', async () => {
    const { send } = fakeFetch(jsonResponse(500, { oops: true }))

    const failure = await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send }).catch(
      (error: unknown) => error,
    )

    expect(failure).toBeInstanceOf(GatewayError)
    expect(failure).toMatchObject({ status: 500, code: 'internal' })
  })

  it('没有会话令牌时直接失败，不发出请求', async () => {
    // 让它撞上 401 只会把「令牌没注入」伪装成「认证失败」。
    const { send, calls } = fakeFetch(jsonResponse(200, {}))

    const failure = await requestGateway('/v1/gateway/status', { token: '', fetchImpl: send }).catch(
      (error: unknown) => error,
    )

    expect(failure).toBeInstanceOf(GatewayError)
    expect(calls).toHaveLength(0)
  })

  it('拒绝 /v1 之外的路径', async () => {
    const { send, calls } = fakeFetch(jsonResponse(200, {}))

    const failure = await requestGateway('/internal/debug', { token: TOKEN, fetchImpl: send }).catch(
      (error: unknown) => error,
    )

    expect(failure).toBeInstanceOf(GatewayError)
    expect(calls).toHaveLength(0)
  })

  it('响应不是 JSON 时报错而不是抛出解析异常', async () => {
    const { send } = fakeFetch(new Response('<!doctype html>', { status: 200 }))

    const failure = await requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send }).catch(
      (error: unknown) => error,
    )

    expect(failure).toBeInstanceOf(GatewayError)
  })

  it('空响应体解析为 null', async () => {
    const { send } = fakeFetch(new Response('', { status: 200 }))

    await expect(requestGateway('/v1/gateway/status', { token: TOKEN, fetchImpl: send })).resolves.toBeNull()
  })
})
