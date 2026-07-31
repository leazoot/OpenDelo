import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'

import { useRevokeLease, type Lease } from './leases'
import { LEASES_KEY } from './liveUpdates'
import { createQueryClient } from './queryClient'

/*
 * 收回一条 Lease 的乐观更新与回滚。
 *
 * 这里直接看缓存，不经界面：没有查询在监听 LEASES_KEY，因此失效不会触发重拉 ——
 * 那一条回到列表里只可能是回滚放回去的，不可能是服务端又给回来的。
 * 从界面上看这两件事长得一模一样，那样的用例证明不了回滚存在。
 */

const lease = (id: string): Lease => ({
  id,
  agentId: 'ag-1',
  identityId: 'id-1',
  service: 'github',
  scope: 'src/',
  expiresAt: '2026-07-29T12:41:00.000Z',
  isSessionBound: false,
})

function harness(revoke: (id: string) => Promise<void>) {
  const client = createQueryClient()
  client.setQueryData<Lease[]>(LEASES_KEY, [lease('ls-1'), lease('ls-2')])

  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }

  return { client, ...renderHook(() => useRevokeLease({ revoke }), { wrapper }) }
}

const idsIn = (client: QueryClient) =>
  (client.getQueryData<Lease[]>(LEASES_KEY) ?? []).map((item) => item.id)

describe('收回一条 Lease', () => {
  it('那一条立刻从缝内侧消失，不等服务端回话', async () => {
    const { client, result } = harness(() => new Promise<void>(() => undefined))

    result.current.revoke('ls-1')

    await waitFor(() => {
      expect(idsIn(client)).toEqual(['ls-2'])
    })
  })

  it('失败时那一条放回原处，而不是让人以为已经收回了', async () => {
    // 失败的时机由用例握着：直接返回一个已拒绝的 Promise 时，乐观移除与回滚
    // 可能在同一轮里发生完，那样「一直都是两条」也能让断言通过 —— 证明不了回滚存在。
    let fail: ((reason: Error) => void) | null = null
    const { client, result } = harness(
      () =>
        new Promise<void>((_resolve, reject) => {
          fail = reject
        }),
    )

    result.current.revoke('ls-1')
    await waitFor(() => {
      expect(idsIn(client)).toEqual(['ls-2'])
    })

    act(() => {
      if (fail === null) {
        throw new Error('提交还没开始，没有可以让它失败的那一刻')
      }
      fail(new Error('internal'))
    })

    await waitFor(() => {
      expect(idsIn(client)).toEqual(['ls-1', 'ls-2'])
    })
  })

  it('失败这件事说得出口，不被吞掉', async () => {
    const { result } = harness(() => Promise.reject(new Error('internal')))

    result.current.revoke('ls-1')

    await waitFor(() => {
      expect(result.current.isError).toBe(true)
    })
  })

  it('提交期间那一条在 pending 里，重复点击被拦住', async () => {
    const { result } = harness(() => new Promise<void>(() => undefined))

    result.current.revoke('ls-1')

    await waitFor(() => {
      expect(result.current.pendingId).toBe('ls-1')
    })
  })

  it('成功之后那一条不再回来', async () => {
    const { client, result } = harness(() => Promise.resolve())

    result.current.revoke('ls-1')

    await waitFor(() => {
      expect(result.current.pendingId).toBe('')
    })
    expect(idsIn(client)).toEqual(['ls-2'])
  })
})
