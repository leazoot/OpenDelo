import { useEffect, useState } from 'react'

/** 界面上「现在几点」的刷新间隔。剩余时间按秒走（REQ-LEASE-003 AC1）。 */
export const TICK_INTERVAL_MS = 1_000

/**
 * 每秒推进一次的当前时刻。
 *
 * 组件因此不必各自记时间：谁需要「还剩多久」，谁调用这个 hook，
 * 剩下的都是纯计算。
 */
export function useNow(intervalMs = TICK_INTERVAL_MS): number {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = setInterval(() => {
      setNow(Date.now())
    }, intervalMs)
    return () => {
      clearInterval(timer)
    }
  }, [intervalMs])

  return now
}
