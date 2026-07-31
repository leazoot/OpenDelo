import { describe, expect, it } from 'vitest'

import { isActionOffered, LEARNING_ACTIONS } from './decisions'

/*
 * 界面上「这条允许做什么」（REQ-APPROVAL-002）。
 */

const ALL = ['allow_once', 'allow_until_task_end', 'auto_allow_in_project', 'always_ask', 'deny']

describe('后端给出的可选操作', () => {
  it('清单里有的才允许', () => {
    expect(isActionOffered(ALL, 'allow-task')).toBe(true)
    expect(isActionOffered(ALL, 'deny')).toBe(true)
  })

  it('清单里没有的一律不允许', () => {
    expect(isActionOffered(['deny'], 'allow-task')).toBe(false)
    expect(isActionOffered(['deny'], 'allow-once')).toBe(false)
  })

  it('拿不到清单时一律不允许（Fail Closed）', () => {
    // 空清单意味着不知道这个风险等级下什么是允许的。此时按钮亮着就是在猜。
    for (const action of ['allow-once', 'allow-task', 'allow-project', 'always-ask', 'deny'] as const) {
      expect(isActionOffered([], action), action).toBe(false)
    }
  })

  it('第五种操作有自己的端点路径（PRD §13.2）', () => {
    // 它一度没有路由，界面因此渲染不出这个按钮。
    expect(isActionOffered(ALL, 'always-ask')).toBe(true)
    expect(isActionOffered(['deny'], 'always-ask')).toBe(false)
  })

  it('认不出的操作名不匹配任何端点', () => {
    expect(isActionOffered(['something_new'], 'allow-task')).toBe(false)
  })

  it('学习类操作是那两个 —— 高风险不提供它们', () => {
    expect([...LEARNING_ACTIONS].sort()).toEqual(['allow-project', 'allow-task'])
  })
})
