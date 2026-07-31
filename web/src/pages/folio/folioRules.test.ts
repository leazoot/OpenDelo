import { describe, expect, it } from 'vitest'

import { folioOf, type FolioPayload } from '../../data/folio'

import { mayDecide, needsStrongAuth } from './folioRules'

/*
 * 卷宗上能做什么（REQ-APPROVAL-002 / 005）。
 *
 * 按钮与按键共用这里的判断，因此这一组用例同时守住两条路。
 */

const ALL = ['allow_once', 'allow_until_task_end', 'auto_allow_in_project', 'deny']

const folioAt = (level: string, status = 'awaiting_approval'): FolioPayload => ({
  id: 'rq-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  service: 'github',
  operation: 'write',
  resource: { path: 'src/' },
  desired_change: null,
  reason: '',
  status,
  change_preview: null,
  withheld_operations: null,
  created_at: '2026-07-29T09:00:00Z',
  decision: {
    verdict: 'require_approval',
    risk_level: level,
    risk_factors: [],
    identity_id: 'id-1',
    reason_code: 'requires_confirmation',
    resolved_scope: {},
  },
})

const input = (level: string, status = 'awaiting_approval', available = ALL, strongAuthDone = false) => ({
  folio: folioOf(folioAt(level, status)),
  approvalId: 'ap-1',
  availableActions: available,
  strongAuthDone,
})

describe('需要强认证吗', () => {
  it('高风险要', () => {
    expect(needsStrongAuth(folioOf(folioAt('high')))).toBe(true)
  })

  it('低风险与中风险不要', () => {
    expect(needsStrongAuth(folioOf(folioAt('low')))).toBe(false)
    expect(needsStrongAuth(folioOf(folioAt('medium')))).toBe(false)
  })

  it('认不出的等级按要处理 —— 风险未知一律走更严的那条路', () => {
    expect(needsStrongAuth(folioOf(folioAt('')))).toBe(true)
    expect(needsStrongAuth(folioOf(folioAt('catastrophic')))).toBe(true)
  })
})

describe('这个决定能不能提交', () => {
  it('中风险且后端提供时，四个操作都能', () => {
    for (const action of ['allow-task', 'allow-once', 'allow-project', 'deny'] as const) {
      expect(mayDecide(input('medium'), action)).toBe(true)
    }
  })

  it('高风险在解锁之前只剩拒绝', () => {
    expect(mayDecide(input('high'), 'allow-task')).toBe(false)
    expect(mayDecide(input('high'), 'allow-once')).toBe(false)
    expect(mayDecide(input('high'), 'allow-project')).toBe(false)
    expect(mayDecide(input('high'), 'deny')).toBe(true)
  })

  it('高风险在保险库解开之后放得出去 —— 强认证是那道门唯一的钥匙', () => {
    for (const action of ['allow-task', 'allow-once', 'allow-project', 'deny'] as const) {
      expect(mayDecide(input('high', 'awaiting_approval', ALL, true), action)).toBe(true)
    }
  })

  it('解锁不会替后端补上没提供的操作', () => {
    expect(mayDecide(input('high', 'awaiting_approval', ['deny'], true), 'allow-task')).toBe(false)
  })

  it('解锁不会让已经有结论的那条重新可决定', () => {
    expect(mayDecide(input('high', 'approved', ALL, true), 'allow-once')).toBe(false)
  })

  it('后端没提供的操作不能提交，哪怕风险很低', () => {
    expect(mayDecide(input('low', 'awaiting_approval', ['deny']), 'allow-task')).toBe(false)
  })

  it('拿不到清单时一个放行都不给（Fail Closed）', () => {
    for (const action of ['allow-task', 'allow-once', 'allow-project', 'deny'] as const) {
      expect(mayDecide(input('low', 'awaiting_approval', []), action)).toBe(false)
    }
  })

  it('已经有结论的那条一个都不能', () => {
    expect(mayDecide(input('low', 'approved'), 'deny')).toBe(false)
    expect(mayDecide(input('low', 'approved'), 'allow-task')).toBe(false)
  })

  it('找不到审批项时一个都不能 —— 没有主键就不知道该决定哪一条', () => {
    expect(mayDecide({ ...input('low'), approvalId: '' }, 'deny')).toBe(false)
  })
})
