import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'

import { subscribeToEvents, type GatewayEvent } from './eventStream'
import { GATEWAY_STATUS_KEY } from './gatewayStatus'
import {
  approvalListSchema,
  mergePassage,
  passageOfApproval,
  passageOfRequest,
  PASSAGES_KEY,
  type Passage,
} from './passages'

/*
 * 把推送写进 Query 的缓存。
 *
 * 不维护第二份列表：界面读的永远是 Query 里的那一份，推送只是让它更新得更快。
 */

/** 断线后等多久重连。服务端已经用 SSE 的 retry 字段建议过 2 秒，这里保持一致。 */
export const RECONNECT_DELAY_MS = 2_000

/** Lease 的查询键。lease 事件让持有它的页面重新拉一次。 */
export const LEASES_KEY = ['leases'] as const

const approvalPayloadSchema = approvalListSchema.shape.items.element
const requestPayloadSchema = approvalPayloadSchema.shape.request.unwrap()

/**
 * 一条事件带来的 Passage。
 *
 * `passage` 这一类事件有两种形状：审批被决定时推的是审批项，请求被取消时推的是
 * 请求本身。两者都试一遍而不是让调用方去猜，认不出就返回 null。
 */
export function passageOfEvent(event: GatewayEvent): Passage | null {
  const asApproval = approvalPayloadSchema.safeParse(event.data)
  if (asApproval.success) {
    return passageOfApproval(asApproval.data)
  }
  const asRequest = requestPayloadSchema.safeParse(event.data)
  return asRequest.success ? passageOfRequest(asRequest.data) : null
}

export function applyEvent(client: QueryClient, event: GatewayEvent): void {
  switch (event.type) {
    case 'arrival':
    case 'passage': {
      const passage = passageOfEvent(event)
      if (passage === null) {
        return
      }
      // **先取消正在飞的那次拉取。** 页面刚打开时列表拉取还没回来，这时写进
      // 缓存的推送会被随后落地的那份旧结果盖掉 —— 而之后没有人再拉一次，
      // 于是缝前那条请求再也不会出现，界面停在「无人等待」。
      //
      // 本机上这个窗口是毫秒级，CI 上够宽：2026-08-07 起它在那里反复红，
      // 而同一份用例在开发机上一直是绿的。
      void client.cancelQueries({ queryKey: PASSAGES_KEY })
      client.setQueryData<Passage[]>(PASSAGES_KEY, (existing) => mergePassage(existing ?? [], passage))
      return
    }
    case 'lease':
      void client.invalidateQueries({ queryKey: LEASES_KEY })
      return
    case 'gateway':
      void client.invalidateQueries({ queryKey: GATEWAY_STATUS_KEY })
      return
  }
}

export interface UseLiveUpdatesOptions {
  /** 覆盖订阅实现，只在测试里用。 */
  readonly subscribe?: typeof subscribeToEvents
  /** 覆盖重连间隔，只在测试里用。 */
  readonly reconnectDelayMs?: number
}

/**
 * 订阅事件流并在断线后重连。
 *
 * 断线不是错误而是常态（休眠、网关重启、标签页被节流），所以循环里不抛错。
 * 每次重连后让已有查询失效重新拉一遍：服务端不重放事件，断开期间发生的事
 * 只能靠这一次拉取补回来（REQ-API-002 的「断线可重连」）。
 */
export function useLiveUpdates(options: UseLiveUpdatesOptions = {}): void {
  const client = useQueryClient()
  const subscribe = options.subscribe ?? subscribeToEvents
  const reconnectDelayMs = options.reconnectDelayMs ?? RECONNECT_DELAY_MS

  useEffect(() => {
    // 卸载与「还连着吗」只有一个答案：signal。两个标志迟早会说两句不同的话。
    const controller = new AbortController()
    const stopped = () => controller.signal.aborted

    const listen = async () => {
      while (!stopped()) {
        const ended = await connectOnce(client, subscribe, controller.signal)
        if (!ended.reconnect) {
          return
        }
        await sleep(reconnectDelayMs, controller.signal)
        if (stopped()) {
          return
        }
        await client.invalidateQueries({ queryKey: PASSAGES_KEY })
      }
    }

    void listen()
    return () => {
      controller.abort()
    }
  }, [client, subscribe, reconnectDelayMs])
}

/**
 * 连一次。返回是否应该重连。
 *
 * 取消是我们自己发起的（组件卸载），不重连；其余任何结束方式都重连。
 */
async function connectOnce(
  client: QueryClient,
  subscribe: typeof subscribeToEvents,
  signal: AbortSignal,
): Promise<{ reconnect: boolean }> {
  try {
    await subscribe({
      signal,
      onEvent: (event) => {
        applyEvent(client, event)
      },
    })
  } catch {
    // 连不上与连着连着断了在这里是同一件事：等一会儿再来。
    // 「连不上 Gateway」这句话由 gatewayStatus 那条链路说出口，不在这里重复报一次。
    return { reconnect: !signal.aborted }
  }
  return { reconnect: !signal.aborted }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
  })
}
