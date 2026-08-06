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
    // 真实形状：收敛出来的 Scope，资源是嵌套对象。原来这里是扁平的
    // `{repo, path}`，服务端从来没有那样发过 —— 正是它把 describeScope
    // 的缺陷藏了起来。
    resource_scope: {
      service: 'github',
      resource: { owner: 'runcoor', repo: 'opendelo' },
      operation: 'read_repository',
    },
    expires_at: at(60_000),
    status: 'active',
    is_session_bound: true,
  }

  it('带上服务、Scope 与到期时刻', () => {
    const parsed = leaseListSchema.parse({ items: [payload] })
    const lease = leaseOf(parsed.items[0] ?? payload)

    expect(lease.service).toBe('github')
    expect(lease.scope).toBe('runcoor · opendelo · read_repository')
    expect(lease.isSessionBound).toBe(true)
  })

  it('形状不对时解析失败，而不是当成一条空列表', () => {
    expect(() => leaseListSchema.parse({ items: [{ id: 'ls-1' }] })).toThrow()
    expect(() => leaseListSchema.parse({})).toThrow()
  })

  it('Scope 的描述认得字符串与对象，认不出的形状返回空串', () => {
    expect(describeScope('~/notes/')).toBe('~/notes/')
    // 认不出资源与操作的对象返回空串，而不是把它的每个值都铺出来 ——
    // 后者正是缝内侧那排标签变成一行长串的原因。
    expect(describeScope({ a: 'x', b: 'y' })).toBe('')
    expect(describeScope(7)).toBe('')
  })
})

/*
 * 授权标签上显示什么（回归）。
 *
 * `describeScope` 原本把 Scope 对象里**每一个**字符串值都拼出来。真实的 Scope 有
 * 十一个维度：两个 ULID、身份 ID、账号、资源、操作、两个时间戳、环境、风险上限 ——
 * 缝内侧那一排小标签因此变成一行读不懂的长串，还会溢出到框外
 * （2026-08-04 人工验收撞出）。
 *
 * 标签要回答的只有一个问题：**这条授权覆盖的是什么**。剩下的在 Inspector 与卷宗里。
 */
describe('授权标签上的 Scope', () => {
  const realistic = {
    agent_id: '01KZ6SP6QDDQ7SCVAY1YDHNMZB',
    workspace_id: '01KZ6SP6QDDQ7SCVAY1VANZ18Y',
    service: 'github',
    identity_id: '01KZ6SNVAAENTPHD0K8HHEBK2S',
    account: 'work',
    resource: { owner: 'Dline-R', repo: 'aiba-app' },
    resource_key: 'owner=Dline-R;repo=aiba-app',
    operation: 'create_issue',
    not_before: '2026-08-05T00:20:48.411917Z',
    expires_at: '2026-08-05T00:35:48.411917Z',
    environment: 'production',
    risk_ceiling: 'medium',
  }

  it('只说这条授权覆盖了什么', () => {
    expect(describeScope(realistic)).toBe('Dline-R · aiba-app · create_issue')
  })

  it('不把主键、时间戳与内部维度铺到标签上', () => {
    const text = describeScope(realistic)

    for (const leaked of [
      '01KZ6SP6QDDQ7SCVAY1YDHNMZB',
      '01KZ6SP6QDDQ7SCVAY1VANZ18Y',
      '01KZ6SNVAAENTPHD0K8HHEBK2S',
      '2026-08-05T00:20:48.411917Z',
      '2026-08-05T00:35:48.411917Z',
      'production',
      'medium',
      'owner=Dline-R;repo=aiba-app',
    ]) {
      expect(text, `${leaked} 不该出现在缝内侧那一排小标签上`).not.toContain(leaked)
    }
  })

  it('取不到资源时退回操作，而不是空着', () => {
    expect(describeScope({ operation: 'read_repository' })).toBe('read_repository')
  })
})
