import { describe, expect, it } from 'vitest'

import { createEventParser, subscribeToEvents } from './eventStream'

/*
 * SSE 的解析（REQ-API-002 AC2）。
 */

const arrival = (id: string) =>
  `event: arrival\ndata: ${JSON.stringify({ type: 'arrival', data: { id }, at: '2026-07-29T00:00:00Z' })}\n\n`

describe('事件流的解析', () => {
  it('认出一条事件', () => {
    const parse = createEventParser()

    const events = parse(arrival('01JA'))

    expect(events).toHaveLength(1)
    expect(events[0]?.type).toBe('arrival')
  })

  it('一个分片里的多条事件全部认出', () => {
    const parse = createEventParser()

    expect(parse(arrival('01JA') + arrival('01JB'))).toHaveLength(2)
  })

  it('被切成两半的事件在后半到达时才认出，而不是丢掉', () => {
    // TCP 不保证一条事件落在同一个分片里。丢掉半条的后果是界面永远少一条。
    const whole = arrival('01JA')
    const parse = createEventParser()

    expect(parse(whole.slice(0, 20))).toHaveLength(0)
    expect(parse(whole.slice(20))).toHaveLength(1)
  })

  it('心跳与 retry 行不产生事件', () => {
    const parse = createEventParser()

    expect(parse('retry: 2000\n\n: keep-alive\n\n')).toHaveLength(0)
  })

  it('认不出的一条被丢掉，后面的照常认出', () => {
    // 这条流是通知不是账本。让一条坏事件把整条流带断，界面会从此不再更新。
    const parse = createEventParser()

    const events = parse('data: {"type":"nope"}\n\n' + 'data: not json at all\n\n' + arrival('01JA'))

    expect(events).toHaveLength(1)
    expect(events[0]?.type).toBe('arrival')
  })

  it('四类事件都认', () => {
    const parse = createEventParser()

    for (const type of ['arrival', 'passage', 'lease', 'gateway']) {
      const events = parse(`data: ${JSON.stringify({ type, data: {}, at: 'now' })}\n\n`)
      expect(events[0]?.type, type).toBe(type)
    }
  })
})

/*
 * 读流这一段（2026-08-07 的 CI 上撞出来的）。
 *
 * 此前用的是 `body.pipeThrough(new TextDecoderStream())`。Linux WebKit 上
 * 一条事件都收不到，而 chromium 与 firefox 同一份用例全绿 —— 服务端怎么改都没用，
 * 因为流从来没被读到过。这里的用例把那个环境造出来：没有 TextDecoderStream。
 */
describe('订阅事件流', () => {
  const chunks = (...parts: string[]): ReadableStream<Uint8Array> => {
    const encoder = new TextEncoder()
    return new ReadableStream({
      start(controller) {
        for (const part of parts) {
          controller.enqueue(encoder.encode(part))
        }
        controller.close()
      },
    })
  }

  const collect = async (stream: ReadableStream<Uint8Array>): Promise<string[]> => {
    const seen: string[] = []
    await subscribeToEvents({
      signal: new AbortController().signal,
      token: 'session-token',
      fetchImpl: () =>
        Promise.resolve(
          new Response(stream, {
            status: 200,
            headers: { 'content-type': 'text/event-stream' },
          }),
        ),
      onEvent: (event) => seen.push(event.type),
    })
    return seen
  }

  it('不依赖 TextDecoderStream —— 拿掉它照样读得出事件', async () => {
    const original = globalThis.TextDecoderStream
    // @ts-expect-error 造一个没有它的运行环境，这正是 WebKit 那一侧的形状。
    delete globalThis.TextDecoderStream
    try {
      expect(await collect(chunks(arrival('01JA'), arrival('01JB')))).toEqual(['arrival', 'arrival'])
    } finally {
      globalThis.TextDecoderStream = original
    }
  })

  it('一个多字节字符被切在两个 chunk 之间也解得出来', async () => {
    // 逐块独立解码会把它变成替换字符，事件体随之解析失败。
    const whole = new TextEncoder().encode(
      `event: arrival\ndata: ${JSON.stringify({ type: 'arrival', data: { id: '缝前' }, at: '2026-07-29T00:00:00Z' })}\n\n`,
    )
    const cut = Math.floor(whole.length / 2)
    const split = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(whole.slice(0, cut))
        controller.enqueue(whole.slice(cut))
        controller.close()
      },
    })

    expect(await collect(split)).toEqual(['arrival'])
  })

  it('响应没有体时说出来，不装作订阅成功', async () => {
    // 静默返回等于「订阅上了但什么都收不到」，而调用方无从分辨。
    await expect(
      subscribeToEvents({
        signal: new AbortController().signal,
        token: 'session-token',
        fetchImpl: () => Promise.resolve(new Response(null, { status: 200 })),
        onEvent: () => undefined,
      }),
    ).rejects.toThrow(/响应体/)
  })
})
