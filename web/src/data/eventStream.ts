import { z } from 'zod'

import { sendGateway } from './gateway'

/*
 * SSE 的解析与订阅（REQ-API-002 AC2）。
 *
 * 不用浏览器内建的 EventSource：它发不出自定义请求头，而 `/v1` 的每个请求都要带
 * 会话令牌与 `X-Requested-By`（REQ-API-005）。把令牌挪进 URL 才能用 EventSource ——
 * 那样它会进浏览器历史、Referer 与访问日志，正是 REQ-API-005 要避免的。
 * 于是这里用 fetch 拿到流自己解析。
 */

/** 四类事件（REQ-API-002 AC2）。 */
export const gatewayEventSchema = z.object({
  type: z.enum(['arrival', 'passage', 'lease', 'gateway']),
  data: z.unknown(),
  at: z.string().min(1),
})

export type GatewayEvent = z.infer<typeof gatewayEventSchema>

/**
 * 把 SSE 的文本流切成一条条事件。
 *
 * 有状态：调用方按到达顺序把每个分片喂进来，跨分片的半条事件留在缓冲里。
 * 只认 `data:` 行 —— 事件类型已经在 JSON 里，再从 `event:` 行读一遍会得到
 * 两个可能对不上的答案。
 */
export function createEventParser(): (chunk: string) => GatewayEvent[] {
  let buffer = ''

  return (chunk: string): GatewayEvent[] => {
    buffer += chunk
    const events: GatewayEvent[] = []

    // SSE 用空行分隔事件。最后一段可能是半条，留在缓冲里等下一个分片。
    const blocks = buffer.split('\n\n')
    buffer = blocks.pop() ?? ''

    for (const block of blocks) {
      const payload = block
        .split('\n')
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice('data:'.length).trim())
        .join('')
      if (payload === '') {
        // 心跳（`: keep-alive`）与 retry 行走到这里，跳过即可。
        continue
      }
      const parsed = gatewayEventSchema.safeParse(safeJson(payload))
      if (parsed.success) {
        events.push(parsed.data)
      }
      // 解析不了的一条丢掉：这条流是通知不是账本，认不出的事件只意味着
      // 界面少刷新一次，而让它把整条流带断会让界面从此不再更新。
    }
    return events
  }
}

function safeJson(text: string): unknown {
  try {
    const parsed: unknown = JSON.parse(text)
    return parsed
  } catch {
    return null
  }
}

export interface SubscribeOptions {
  readonly signal: AbortSignal
  readonly onEvent: (event: GatewayEvent) => void
  /** 覆盖请求实现，只在测试里用。 */
  readonly fetchImpl?: typeof fetch
  /** 覆盖会话令牌，只在测试里用。 */
  readonly token?: string
}

/**
 * 订阅事件流，直到 signal 被取消或连接断开。
 *
 * 返回时说明这条连接结束了；重连由调用方决定 —— 断线不是错误，而是常态。
 */
export async function subscribeToEvents(options: SubscribeOptions): Promise<void> {
  const request: { signal: AbortSignal; fetchImpl?: typeof fetch; token?: string } = { signal: options.signal }
  if (options.fetchImpl !== undefined) {
    request.fetchImpl = options.fetchImpl
  }
  if (options.token !== undefined) {
    request.token = options.token
  }

  const response = await sendGateway('/v1/events', request)
  const body = response.body
  if (body === null) {
    // 静默返回等于「订阅上了但什么都收不到」，而调用方无从分辨。
    // 这条流是缝前那张卡片的唯一来源，收不到就要说出来（不静默降级）。
    throw new Error('事件流没有响应体，这条连接读不出任何事件')
  }

  const parse = createEventParser()
  // **自己解码，不用 TextDecoderStream。** 后者要经 `pipeThrough` 接在 fetch 的
  // 响应体上，而 WebKit 那一族对它的支持最弱：2026-08-07 的 CI 上 Linux WebKit
  // 一条事件都收不到（chromium 与 firefox 同一份用例全绿），服务端怎么改都没用 ——
  // 因为流从来没有被读到过。`TextDecoder` 是老得多、也普遍得多的那一个。
  //
  // `stream: true` 是必需的：一个多字节字符会被切在两个 chunk 之间，
  // 逐块独立解码会把它变成两个替换字符。
  const reader = body.getReader()
  const decoder = new TextDecoder()
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        return
      }
      for (const event of parse(decoder.decode(value, { stream: true }))) {
        options.onEvent(event)
      }
    }
  } finally {
    reader.releaseLock()
  }
}
