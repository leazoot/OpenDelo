import { describe, expect, it } from 'vitest'

import type { TrustMemory } from '../../data/trustMemories'
import { copyFor } from '../../i18n/copy'

import { alwaysAsked, autoAllowed, bindingsOf, learnedRules, originTextOf, riskPolicies } from './automation'

/*
 * Automation 的六类内容（PRD §16.3、REQ-UI-006）。
 *
 * 这一组用例守的是「页面上写着的每一条都能在记忆里找到出处」——
 * 编出来的规则读起来与真的一模一样。
 */

const zh = copyFor('zh')
const names = new Map([['id-1', 'ops@example.com · production']])

const memory = (over: Partial<TrustMemory> = {}): TrustMemory => ({
  id: 'tm-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  identity_id: 'id-1',
  service: 'github',
  environment: 'production',
  risk_ceiling: 'medium',
  approval_behavior: 'auto_allow',
  created_from: 'ap-1',
  status: 'active',
  invalidation_reason: '',
  expires_at: '2026-08-30T09:00:00Z',
  created_at: '2026-07-26T09:15:30.123Z',
  ...over,
})

describe('已学习授权', () => {
  it('每条都说出自己是怎么来的（REQ-TRUST-001 AC3）', () => {
    const rules = learnedRules([memory()], names, zh)

    expect(rules[0]?.origin).toBe('由你在 2026-07-26 的审批中创建')
  })

  it('认不出的创建时刻不编一个日期', () => {
    expect(originTextOf(memory({ created_at: '' }), zh)).toBe(zh.automationOriginUnknown)
    expect(originTextOf(memory({ created_at: '刚刚' }), zh)).toBe(zh.automationOriginUnknown)
  })

  it('身份写成人能读的样子，查不到时退回主键', () => {
    expect(learnedRules([memory()], names, zh)[0]?.title).toBe('github · ops@example.com · production')
    expect(learnedRules([memory({ identity_id: 'id-9' })], names, zh)[0]?.title).toBe('github · id-9')
  })

  it('失效的记忆不消失，但要说明为什么（REQ-TRUST-004 AC2）', () => {
    const rule = learnedRules([memory({ status: 'invalidated', invalidation_reason: 'scope_expanded' })], names, zh)[0]

    expect(rule?.isInvalidated).toBe(true)
    expect(rule?.invalidationReason).toBe('scope_expanded')
  })
})

describe('两张清单', () => {
  it('自动允许里只有仍然生效且确实是自动允许的那些', () => {
    const rules = learnedRules(
      [
        memory({ id: 'tm-1' }),
        memory({ id: 'tm-2', approval_behavior: 'always_ask' }),
        memory({ id: 'tm-3', status: 'invalidated' }),
      ],
      names,
      zh,
    )

    expect(autoAllowed(rules).map((rule) => rule.id)).toEqual(['tm-1'])
  })

  it('认不出的行为落进「每次都问」这一侧，而不是自动允许', () => {
    const rules = learnedRules([memory({ approval_behavior: 'sometimes' })], names, zh)

    expect(autoAllowed(rules)).toEqual([])
    expect(alwaysAsked(rules).map((rule) => rule.id)).toEqual(['tm-1'])
  })
})

describe('项目与身份绑定', () => {
  it('同一个项目与身份合成一条，并数出它有几条规则', () => {
    const bindings = bindingsOf([
      memory({ id: 'tm-1' }),
      memory({ id: 'tm-2' }),
      memory({ id: 'tm-3', workspace_id: 'ws-2' }),
    ])

    expect(bindings).toHaveLength(2)
    expect(bindings.find((binding) => binding.workspaceId === 'ws-1')?.ruleCount).toBe(2)
  })

  it('失效的记忆不算作一条绑定', () => {
    expect(bindingsOf([memory({ status: 'invalidated' })])).toEqual([])
  })
})

describe('风险策略（REQ-DECIDE-003）', () => {
  it('高风险那一条与模式无关，且标着不可关闭（AC3）', () => {
    for (const mode of ['cautious', 'balanced', 'automatic', '']) {
      const high = riskPolicies(mode, zh).find((policy) => policy.level === 'high')

      expect(high?.text).toBe(zh.automationHighPolicy)
      expect(high?.isFixed).toBe(true)
    }
  })

  it('三种模式各自说出自己的低风险与中风险规则', () => {
    expect(riskPolicies('balanced', zh)[0]?.text).toBe(zh.automationLowPolicy('balanced'))
    expect(riskPolicies('automatic', zh)[1]?.text).toBe(zh.automationMediumPolicy('automatic'))
    expect(riskPolicies('cautious', zh)[1]?.text).toBe(zh.automationMediumPolicy('cautious'))
  })

  it('认不出的模式按最严的那一档显示 —— 说松了会让用户以为某件事不会自动发生', () => {
    expect(riskPolicies('', zh)[1]?.text).toBe(zh.automationMediumPolicy('cautious'))
    expect(riskPolicies('turbo', zh)[0]?.text).toBe(zh.automationLowPolicy('cautious'))
  })
})
