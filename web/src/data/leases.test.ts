import { describe, expect, it } from 'vitest'

import {
  describeScope,
  ENDING_SOON_MS,
  formatRemaining,
  isEndingSoon,
  leaseListSchema,
  leaseOf,
  remainingMillis,
} from './leases'

/*
 * 剩余时间的计量（REQ-LEASE-003 AC1、AC3）。
 */

const NOW = Date.parse('2026-07-29T12:00:00.000Z')
const at = (offsetMs: number) => new Date(NOW + offsetMs).toISOString()

describe('剩余时间', () => {
  it('随时间递减，每一秒的误差都在 2 秒以内', () => {
    // AC1：这里逐秒推进 120 次，把显示值换算回秒数与真实剩余比对。
    const expires = at(10 * 60_000)
    for (let tick = 0; tick < 120; tick++) {
      const now = NOW + tick * 1_000
      const left = remainingMillis(expires, now)
      const shown = formatRemaining(left)
      const shownMs = shown.endsWith('m')
        ? Number(shown.slice(0, -1)) * 60_000
        : Number(shown.slice(0, -1)) * 1_000

      // 分钟是向上取整的，因此显示值不小于真实值、且不超过真实值一分钟。
      expect(shownMs).toBeGreaterThanOrEqual(left)
      expect(shownMs - left).toBeLessThan(60_000)
    }
  })

  it('一分钟以内说到秒，误差在一秒以内', () => {
    for (let left = 1_000; left < ENDING_SOON_MS; left += 1_000) {
      const shown = formatRemaining(left)
      expect(shown.endsWith('s'), shown).toBe(true)
      expect(Math.abs(Number(shown.slice(0, -1)) * 1_000 - left)).toBeLessThan(1_000)
    }
  })

  it('已经过期与认不出的时刻都按 0 处理，不显示负数也不假装还早', () => {
    expect(remainingMillis(at(-5_000), NOW)).toBe(0)
    expect(remainingMillis('not a time', NOW)).toBe(0)
    expect(formatRemaining(0)).toBe('0s')
  })

  it('不到 60 秒时进入将尽状态（AC3）', () => {
    expect(isEndingSoon(59_999)).toBe(true)
    expect(isEndingSoon(60_000)).toBe(false)
    expect(isEndingSoon(0)).toBe(true)
  })
})

describe('Lease 的解析', () => {
  const payload = {
    id: 'ls-1',
    agent_id: 'ag-1',
    identity_id: 'id-1',
    service: 'github',
    resource_scope: { repo: 'runcoor/opendelo', path: 'src/' },
    expires_at: at(60_000),
    status: 'active',
    is_session_bound: true,
  }

  it('带上服务、Scope 与到期时刻', () => {
    const parsed = leaseListSchema.parse({ items: [payload] })
    const lease = leaseOf(parsed.items[0] ?? payload)

    expect(lease.service).toBe('github')
    expect(lease.scope).toContain('runcoor/opendelo')
    expect(lease.isSessionBound).toBe(true)
  })

  it('形状不对时解析失败，而不是当成一条空列表', () => {
    expect(() => leaseListSchema.parse({ items: [{ id: 'ls-1' }] })).toThrow()
    expect(() => leaseListSchema.parse({})).toThrow()
  })

  it('Scope 的描述认得字符串与对象，认不出的形状返回空串', () => {
    expect(describeScope('~/notes/')).toBe('~/notes/')
    expect(describeScope({ a: 'x', b: 'y' })).toBe('x · y')
    expect(describeScope(7)).toBe('')
  })
})
