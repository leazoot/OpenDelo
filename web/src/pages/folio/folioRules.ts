import { isActionOffered, type DecisionAction } from '../../data/decisions'
import { isDecidable, type Folio } from '../../data/folio'

/*
 * 卷宗上能做什么（REQ-APPROVAL-002 / 005）。
 *
 * 按钮与按键共用这里的判断。两处各写一份，就是让快捷键有一套自己的规则的开始
 * —— 而那套规则迟早会比按钮宽松。
 */

/** 不需要强认证就能放行的风险等级。 */
const OPEN_LEVELS: ReadonlySet<string> = new Set(['low', 'medium'])

/**
 * 这次放行需要强认证。
 *
 * 用白名单而不是 `=== 'high'`：认不出的等级同样要走强认证这条路
 * （风险等级未知一律拒绝）。
 */
export function needsStrongAuth(folio: Folio): boolean {
  return !OPEN_LEVELS.has(folio.riskLevel)
}

export interface DecidableInput {
  readonly folio: Folio
  readonly approvalId: string
  readonly availableActions: readonly string[]
  /** Gateway 已经当面校验过主密码（REQ-APPROVAL-005，用户决定 D-14 方案 C）。 */
  readonly strongAuthDone: boolean
}

/**
 * 这个决定现在能不能提交。
 *
 * 四道门依次是：请求仍在等人 · 找得到审批项 · 后端提供了这个操作 · 强认证。
 * **拒绝不过强认证那道门** —— 那会让用户在拿不到 Passkey 时连「不许」都说不出口
 * （决策链路的既有结论）。
 */
export function mayDecide(input: DecidableInput, action: DecisionAction): boolean {
  const { folio, approvalId, availableActions, strongAuthDone } = input

  if (!isDecidable(folio.status) || approvalId === '') {
    return false
  }
  if (!isActionOffered(availableActions, action)) {
    return false
  }
  return action === 'deny' || !needsStrongAuth(folio) || strongAuthDone
}
