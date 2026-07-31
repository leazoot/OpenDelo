import { cx } from '../../components/cx'
import { StrongAuthPrompt } from '../../components/StrongAuthPrompt'
import type { DecisionAction } from '../../data/decisions'
import type { Folio } from '../../data/folio'
import { useCopy } from '../../i18n/copy'

import styles from './DecisionPanel.module.css'
import { mayDecide, needsStrongAuth } from './folioRules'

/*
 * 卷宗右页底部的决定（REQ-APPROVAL-002）。
 *
 * 按钮的存在与否由后端的 `available_actions` 决定，界面不自己推：
 * 高风险不提供「今后自动允许」这条规则只能有一个答案。拿不到清单时一个
 * 放行按钮都不出现（Fail Closed）。
 */

export interface DecisionPanelProps {
  readonly folio: Folio
  readonly approvalId: string
  readonly availableActions: readonly string[]
  readonly duration: string
  /** 解锁那一段的状态与入口（REQ-APPROVAL-005）。 */
  readonly strongAuth: StrongAuthState
  /** 正在提交的那条 approval 主键；重复点击由它拦住（REQ-APPROVAL-006 AC2）。 */
  readonly pendingId: string
  readonly isError: boolean
  readonly onDecide: (action: DecisionAction) => void
}

/** 强认证在这一页的处境：解没解开、正在校验、失败在哪一档。 */
export interface StrongAuthState {
  readonly isDone: boolean
  readonly isPending: boolean
  readonly failureCode: string
  readonly onUnlock: (masterPassword: string) => void
}

export function DecisionPanel({
  folio,
  approvalId,
  availableActions,
  duration,
  strongAuth,
  pendingId,
  isError,
  onDecide,
}: DecisionPanelProps) {
  const copy = useCopy()
  const input = { folio, approvalId, availableActions, strongAuthDone: strongAuth.isDone }
  const submitting = pendingId !== '' && pendingId === approvalId
  const gated = needsStrongAuth(folio) && !strongAuth.isDone

  const can = (action: DecisionAction) => mayDecide(input, action) && !submitting
  const offered: DecisionAction[] = ['allow-task', 'allow-once', 'allow-project', 'always-ask', 'deny']
  const anyOffered = offered.some((action) => mayDecide(input, action))

  return (
    <div className={styles.panel}>
      <h3 className={styles.title}>{copy.folioDecideTitle}</h3>

      {/*
        高风险的放行要先当面解锁，而不是「按钮点了才发现不行」：说清楚为什么，
        给出解锁的入口，并把拒绝留在原处（REQ-DECIDE-003 AC3）。
      */}
      {gated && (
        <>
          <p className={styles.gate}>{copy.folioHighRiskGate}</p>
          <StrongAuthPrompt
            isPending={strongAuth.isPending}
            failureCode={strongAuth.failureCode}
            onUnlock={strongAuth.onUnlock}
          />
        </>
      )}
      {needsStrongAuth(folio) && strongAuth.isDone && (
        <p className={styles.gate} role="status">
          {copy.folioUnlockDone}
        </p>
      )}
      {!anyOffered && !gated && <p className={styles.gate}>{copy.folioNoActions}</p>}
      {isError && (
        <p className={styles.gate} role="status">
          {copy.folioSettleFailed}
        </p>
      )}

      <button
        type="button"
        className={styles.primary}
        disabled={!can('allow-task')}
        onClick={() => {
          onDecide('allow-task')
        }}
      >
        {copy.folioAllowFor(duration)}
        <span className={styles.key}>A</span>
      </button>

      <div className={styles.row}>
        <button
          type="button"
          className={styles.secondary}
          disabled={!can('allow-once')}
          onClick={() => {
            onDecide('allow-once')
          }}
        >
          {copy.folioAllowOnce}
          <span className={styles.key}>⇧A</span>
        </button>
        <button
          type="button"
          className={cx(styles.secondary, styles.deny)}
          disabled={!can('deny')}
          onClick={() => {
            onDecide('deny')
          }}
        >
          {copy.folioDeny}
          <span className={styles.key}>D</span>
        </button>
      </div>

      {/*
        「今后在此项目自动允许」只在后端提供它时才存在。高风险的 available_actions
        里没有它，因此这一段在高风险的卷宗上连节点都不会出现（REQ-APPROVAL-002 AC1）。
      */}
      {mayDecide(input, 'allow-project') && (
        <button
          type="button"
          className={styles.learn}
          disabled={submitting}
          onClick={() => {
            onDecide('allow-project')
          }}
        >
          {copy.folioAllowProject}
        </button>
      )}

      {/*
        第五种操作（PRD §13.2）。它与上一个按钮相邻但方向相反：一个是「今后别再问」，
        一个是「今后仍然问」。因此它不共用 learn 那一档的强调 —— 收紧不该看起来
        像一次授予。同样只在后端提供它时才存在。
      */}
      {mayDecide(input, 'always-ask') && (
        <button
          type="button"
          className={styles.tighten}
          disabled={submitting}
          onClick={() => {
            onDecide('always-ask')
          }}
        >
          {copy.folioAlwaysAsk}
          <span className={styles.hint}>{copy.folioAlwaysAskHint}</span>
        </button>
      )}
    </div>
  )
}
