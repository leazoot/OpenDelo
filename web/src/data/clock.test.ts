import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { TICK_INTERVAL_MS, useNow } from './clock'

/*
 * 剩余时间按秒走（REQ-LEASE-003 AC1）。这里守住「按秒」这件事本身。
 */

describe('界面时钟', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('每秒推进一次', () => {
    // 间隔一旦放大到分钟，剩余时间的误差就不可能留在 2 秒以内。
    expect(TICK_INTERVAL_MS).toBeLessThanOrEqual(1_000)
  })

  it('时间过去之后读到的是新的时刻，不是挂载那一刻的', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useNow())
    const first = result.current

    act(() => {
      vi.advanceTimersByTime(3 * TICK_INTERVAL_MS)
    })

    expect(result.current - first).toBeGreaterThanOrEqual(3 * TICK_INTERVAL_MS)
  })

  it('卸载之后不再推进，定时器被清掉', () => {
    vi.useFakeTimers()
    const { result, unmount } = renderHook(() => useNow())
    const last = result.current

    unmount()
    act(() => {
      vi.advanceTimersByTime(5 * TICK_INTERVAL_MS)
    })

    expect(result.current).toBe(last)
  })
})
