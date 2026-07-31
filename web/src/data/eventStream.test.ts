import { describe, expect, it } from 'vitest'

import { createEventParser } from './eventStream'

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
