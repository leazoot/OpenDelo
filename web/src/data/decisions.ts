import { useMutation, useQueryClient } from '@tanstack/react-query'

import { requestGateway, type GatewayRequestOptions } from './gateway'
import { LEASES_KEY } from './liveUpdates'
import { PASSAGES_KEY } from './passages'

/*
 * 决策（REQ-APPROVAL-002、REQ-API-004）。
 *
 * 操作由**路径**决定，请求体里没有 action 字段 —— 后端如此，前端也就没有
 * 「传错一个字段就变成另一个决定」的可能。
 */

export type DecisionAction = 'allow-once' | 'allow-task' | 'allow-project' | 'always-ask' | 'deny'

/**
 * 高风险请求不提供学习类操作（REQ-APPROVAL-002）。
 *
 * 这只是界面的第二道门：真正的规则在决策引擎里，后端会在 `available_actions`
 * 里给出这一条允许什么。两边都挡住，是因为界面渲染错了不该变成一次越权。
 */
export const LEARNING_ACTIONS: readonly DecisionAction[] = ['allow-task', 'allow-project']

/** 后端 `available_actions` 里的取值 → 端点路径。 */
const ACTION_PATH: Record<string, DecisionAction> = {
  allow_once: 'allow-once',
  allow_until_task_end: 'allow-task',
  auto_allow_in_project: 'allow-project',
  always_ask: 'always-ask',
  deny: 'deny',
}

/**
 * 这条请求是否允许走某个决定。
 *
 * `available_actions` 为空时一律不允许 —— 拿不到清单就不知道这个风险等级下
 * 什么是允许的，此时放行按钮亮着就是在猜（Fail Closed）。
 */
export function isActionOffered(available: readonly string[], action: DecisionAction): boolean {
  return available.some((name) => ACTION_PATH[name] === action)
}

export async function settleApproval(
  approvalId: string,
  action: DecisionAction,
  options: GatewayRequestOptions = {},
): Promise<void> {
  await requestGateway(`/v1/approvals/${encodeURIComponent(approvalId)}/${action}`, {
    ...options,
    method: 'POST',
  })
}

export interface Settlement {
  readonly approvalId: string
  readonly action: DecisionAction
}

export interface UseSettleOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly settle?: (settlement: Settlement) => Promise<void>
}

export interface SettleView {
  readonly settle: (settlement: Settlement) => void
  /** 正在提交的那条 approval 主键；重复提交由它拦住（REQ-APPROVAL-006 AC2）。 */
  readonly pendingId: string
  readonly isError: boolean
}

/**
 * 提交一次决定。
 *
 * 不做乐观更新：决定的结果由决策链路给出（可能签出 Lease，也可能因为并发
 * 已被别处处理而返回首次结果），界面先画一个自己以为的结局，等真结果回来时
 * 就要在用户眼前改口。提交期间按钮进入 pending，重复点击不会产生第二次决策。
 */
export function useSettleApproval(options: UseSettleOptions = {}): SettleView {
  const client = useQueryClient()
  const send = options.settle ?? ((settlement: Settlement) => settleApproval(settlement.approvalId, settlement.action))

  const mutation = useMutation({
    mutationFn: send,
    onSettled: () => {
      void client.invalidateQueries({ queryKey: PASSAGES_KEY })
      void client.invalidateQueries({ queryKey: LEASES_KEY })
    },
  })

  return {
    settle: (settlement: Settlement) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(settlement)
    },
    pendingId: mutation.isPending ? mutation.variables.approvalId : '',
    isError: mutation.isError,
  }
}
