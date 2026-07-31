import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'

import type { GatewayEvent } from './eventStream'
import { applyEvent, passageOfEvent, useLiveUpdates } from './liveUpdates'
import { PASSAGES_KEY, type Passage } from './passages'
import { createQueryClient } from './queryClient'

const requestPayload = (id: string, verdict: string | null) => ({
  id,
  agent_id: 'ag-1',
  service: 'github',
  operation: 'read',
  resource: { path: 'src/' },
  status: 'decided',
  created_at: '2026-07-29T00:00:00Z',
  decision: verdict === null ? null : { verdict, risk_level: 'low', reason_code: 'low_risk' },
})

const approvalPayload = (requestId: string) => ({
  id: 'ap-1',
  status: 'approved',
  created_at: '2026-07-29T00:00:00Z',
  request: requestPayload(requestId, 'allow'),
  decision: { verdict: 'allow', risk_level: 'low', reason_code: 'trust_memory_match' },
})

const event = (type: GatewayEvent['type'], data: unknown): GatewayEvent => ({
  type,
  data,
  at: '2026-07-29T00:00:00Z',
})

describe('事件 → Passage', () => {
  it('认得请求形状的载荷（arrival 与取消）', () => {
    expect(passageOfEvent(event('arrival', requestPayload('rq-1', 'allow')))?.id).toBe('rq-1')
  })

  it('认得审批形状的载荷（决定之后的 passage）', () => {
    const mapped = passageOfEvent(event('passage', approvalPayload('rq-2')))

    expect(mapped?.id).toBe('rq-2')
    expect(mapped?.approvalId).toBe('ap-1')
  })

  it('认不出的载荷返回 null，不往列表里塞半条', () => {
    expect(passageOfEvent(event('arrival', { nothing: true }))).toBeNull()
  })
})

describe('把事件写进缓存', () => {
  it('arrival 让请求出现在缝前', () => {
    const client = createQueryClient()

    applyEvent(client, event('arrival', requestPayload('rq-1', null)))

    expect(client.getQueryData<Passage[]>(PASSAGES_KEY)).toHaveLength(1)
  })

  it('同一次请求的 arrival 与 passage 只留一行', () => {
    const client = createQueryClient()

    applyEvent(client, event('arrival', requestPayload('rq-1', null)))
    applyEvent(client, event('passage', approvalPayload('rq-1')))

    const list = client.getQueryData<Passage[]>(PASSAGES_KEY)
    expect(list).toHaveLength(1)
    expect(list?.[0]?.verdict).toBe('allowed')
  })

  it('认不出的载荷不改动缓存', () => {
    const client = createQueryClient()
    applyEvent(client, event('arrival', requestPayload('rq-1', null)))

    applyEvent(client, event('passage', { garbage: true }))

    expect(client.getQueryData<Passage[]>(PASSAGES_KEY)).toHaveLength(1)
  })
})

describe('订阅与重连', () => {
  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={createQueryClient()}>{children}</QueryClientProvider>
  }

  it('断线之后自己再连一次，而不是从此不再更新', async () => {
    // 服务端不重放事件，重连之后靠一次 REST 拉取对齐（REQ-API-002）。
    let attempts = 0
    const subscribe = () => {
      attempts += 1
      return Promise.resolve()
    }

    renderHook(() => { useLiveUpdates({ subscribe, reconnectDelayMs: 1 }) }, { wrapper })

    await waitFor(() => {
      expect(attempts).toBeGreaterThan(1)
    })
  })

  it('连不上时也继续重试，而不是停在第一次失败', async () => {
    let attempts = 0
    const subscribe = () => {
      attempts += 1
      return Promise.reject(new Error('connection refused'))
    }

    renderHook(() => { useLiveUpdates({ subscribe, reconnectDelayMs: 1 }) }, { wrapper })

    await waitFor(() => {
      expect(attempts).toBeGreaterThan(1)
    })
  })

  it('卸载之后不再重连', async () => {
    let attempts = 0
    const subscribe = () => {
      attempts += 1
      return Promise.resolve()
    }

    const { unmount } = renderHook(() => { useLiveUpdates({ subscribe, reconnectDelayMs: 1 }) }, { wrapper })
    await waitFor(() => {
      expect(attempts).toBeGreaterThan(0)
    })

    unmount()
    const settled = attempts
    await new Promise((resolve) => setTimeout(resolve, 20))

    expect(attempts).toBeLessThanOrEqual(settled + 1)
  })
})
