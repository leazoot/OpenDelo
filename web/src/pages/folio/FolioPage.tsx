import { useCallback, useEffect, useRef, type HTMLAttributes, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'

import { reasonTextOf, riskTextOf } from '../../boundary/passageText'
import { Seam } from '../../boundary/Seam'
import { cx } from '../../components/cx'
import { useAgentNames } from '../../data/agents'
import { useSettleApproval, type DecisionAction } from '../../data/decisions'
import { isSettled, useFolio, type ChangeField, type Folio } from '../../data/folio'
import { useGatewayStatus } from '../../data/gatewayStatus'
import { useIdentityLabels } from '../../data/identities'
import { usePassages } from '../../data/passages'
import { useUnlockVault } from '../../data/vault'
import { useCopy } from '../../i18n/copy'
import { FOLD_STATE } from '../gate/foldState'
import { useGateKeys, type GateKey } from '../gate/useGateKeys'

import { DecisionPanel, type StrongAuthState } from './DecisionPanel'
import styles from './FolioPage.module.css'
import { mayDecide } from './folioRules'
import { consequencesOf, durationTextOf, limitTextOf, riskFactorTextOf, scopeResourceTextOf } from './folioText'

/** 按键 → 决定。`↵` 与 `Esc` 不在这里，它们不是决定。 */
const KEY_ACTIONS: Partial<Record<GateKey, DecisionAction>> = {
  allow: 'allow-task',
  allowOnce: 'allow-once',
  deny: 'deny',
}

/**
 * Access Folio：缝上切开的一道口子，左页是「谁在敲缝」，右页是「放行之后」。
 *
 * 它是路由态而不是弹窗（REQ-APPROVAL-004）：浏览器后退键与 `Esc` 都只是
 * 折回缝内，**不产生任何决策**。链接因此也可以交给同一条缝前的另一个窗口。
 *
 * 决定本身由右页的 `DecisionPanel` 发出，高风险的当面解锁也在那里；这一页
 * 负责的是把该看见的十一项摆齐，以及把按键翻成与按钮同一条判断。
 */
export function FolioPage() {
  const copy = useCopy()
  const navigate = useNavigate()
  const { id = '' } = useParams()
  const { folio, isLoading, isError, isMissing } = useFolio(id)

  // 审批项的主键与可用操作只在 `GET /v1/approvals` 里；请求详情里没有。
  // 用的是同一个 query key，因此外壳已经拉过的那一份直接命中缓存。
  const { passages } = usePassages()
  const passage = passages.find((item) => item.id === id) ?? null
  const approvalId = passage?.approvalId ?? ''
  const availableActions = passage?.availableActions ?? []

  const { settle, pendingId, isError: settleFailed } = useSettleApproval()
  // 强认证的结论由 Gateway 给出：这里记住的只是「这一次解锁请求成功过」，
  // 真正的判断在放行那一刻由后端自己看保险库（用户决定 D-14 方案 C）。
  const { unlock, isUnlocked, isPending: unlocking, failureCode } = useUnlockVault()

  const strongAuth: StrongAuthState = {
    isDone: isUnlocked,
    isPending: unlocking,
    failureCode,
    onUnlock: (masterPassword: string) => {
      unlock({ masterPassword })
    },
  }

  const fold = useCallback(() => {
    void navigate('/gate', { state: FOLD_STATE })
  }, [navigate])

  const decide = useCallback(
    (action: DecisionAction) => {
      if (folio === null || passage === null) {
        return
      }
      const input = {
        folio,
        approvalId: passage.approvalId,
        availableActions: passage.availableActions,
        strongAuthDone: isUnlocked,
      }
      if (!mayDecide(input, action)) {
        return
      }
      settle({ approvalId: passage.approvalId, action })
    },
    [folio, passage, settle, isUnlocked],
  )

  const onKey = useCallback(
    (intent: GateKey) => {
      if (intent === 'dismiss') {
        fold()
        return
      }
      const action = KEY_ACTIONS[intent]
      if (action !== undefined) {
        // 按键与按钮共用 `mayDecide`：高风险按 A 与点 A 得到同一个答案。
        decide(action)
      }
    },
    [fold, decide],
  )

  useGateKeys(onKey)

  return (
    <div className={styles.folio}>
      {/* 背景对比降一档，但缝仍在原处：用户始终知道自己还在同一条缝前。 */}
      <div className={styles.scrim} aria-hidden="true" />
      <Seam />

      {isLoading && (
        <Sheet aria-busy="true">
          <SheetSkeleton />
        </Sheet>
      )}

      {!isLoading && (isError || folio === null) && (
        <Notice
          title={isMissing ? copy.folioMissingTitle : copy.folioErrorTitle}
          blurb={isMissing ? copy.folioMissingBlurb : copy.folioErrorBlurb}
        />
      )}

      {!isLoading && !isError && folio !== null && (
        <Pages
          folio={folio}
          approvalId={approvalId}
          availableActions={availableActions}
          strongAuth={strongAuth}
          pendingId={pendingId}
          settleFailed={settleFailed}
          onDecide={decide}
        />
      )}
    </div>
  )
}

function Sheet({ children, ...rest }: { children: ReactNode } & HTMLAttributes<HTMLElement>) {
  const copy = useCopy()
  const sheet = useRef<HTMLElement>(null)

  // 卷宗展开时焦点落到它上面，Tab 因此从这一页的开头开始走
  useEffect(() => {
    sheet.current?.focus()
  }, [])

  return (
    <article className={styles.sheet} aria-label={copy.folioLabel} tabIndex={-1} ref={sheet} {...rest}>
      {children}
    </article>
  )
}

interface PagesProps {
  readonly folio: Folio
  readonly approvalId: string
  readonly availableActions: readonly string[]
  readonly strongAuth: StrongAuthState
  readonly pendingId: string
  readonly settleFailed: boolean
  readonly onDecide: (action: DecisionAction) => void
}

function Pages({
  folio,
  approvalId,
  availableActions,
  strongAuth,
  pendingId,
  settleFailed,
  onDecide,
}: PagesProps) {
  const copy = useCopy()
  const agentNames = useAgentNames()
  const identityLabels = useIdentityLabels()
  const { status } = useGatewayStatus()

  const agent = agentNames.get(folio.agentId) ?? folio.agentId
  const host = status === null ? '' : `${status.listen_address}:${String(status.web_api_port)}`
  const factors = folio.riskFactors.map((factor) => riskFactorTextOf(factor, copy)).join(' · ')
  const risk = riskTextOf(folio.riskLevel, copy)

  return (
    <Sheet>
      <section className={styles.arrival}>
        <h2 className={styles.eyebrow}>{copy.folioArrival}</h2>
        <p className={styles.headline}>
          {copy.folioHeadline(agent, folio.operation)} <span className={styles.subject}>{folio.resource}</span>
        </p>

        {isSettled(folio.status) && (
          <p className={styles.settled} role="status">
            {copy.folioSettled}
          </p>
        )}

        <dl className={styles.fields}>
          <Field label={copy.folioFieldAgent} value={agent} />
          <Field label={copy.folioFieldProject} value={folio.workspaceId} isMono />
          <Field label={copy.folioFieldIdentity} value={identityLabels.get(folio.identityId) ?? folio.identityId} />
          <Field label={copy.folioFieldService} value={folio.service} />
          <Field label={copy.folioFieldResource} value={folio.resource} isMono />
          <Change change={folio.change} isPreviewed={folio.isPreviewed} />
          <Field label={copy.folioFieldRisk} value={factors === '' ? risk : `${risk} · ${factors}`} />
          <Field label={copy.inspectorRule} value={reasonTextOf(folio.reasonCode, copy)} isAccent />
        </dl>

        <p className={styles.source}>{copy.folioSource(host)}</p>
      </section>

      <div className={styles.spine} aria-hidden="true">
        <span className={styles.core} />
      </div>

      <section className={styles.consequence}>
        <h2 className={styles.eyebrow}>{copy.folioConsequence}</h2>

        <ul className={styles.outcomes}>
          {consequencesOf(folio, copy).map((item) => (
            <li key={item.text} className={styles.outcome}>
              <span className={cx(styles.mark, styles[item.tone])} aria-hidden="true">
                {item.mark}
              </span>
              {item.text}
            </li>
          ))}
        </ul>

        <div className={styles.lease}>
          <h3 className={styles.eyebrow}>{copy.folioLeaseTitle}</h3>
          <div className={styles.leaseRow}>
            <span className={styles.leaseTab}>
              <span>
                {scopeResourceTextOf(folio)} {folio.scope.operation ?? folio.operation}
              </span>
              <span className={styles.leaseLeft}>{durationTextOf(folio.scope, copy)}</span>
            </span>
            <span className={styles.leaseHint}>
              {limitTextOf(folio.scope, copy)} · {copy.folioLeaseHint}
            </span>
          </div>
        </div>

        <div className={styles.foot}>
          <DecisionPanel
            folio={folio}
            approvalId={approvalId}
            availableActions={availableActions}
            duration={durationTextOf(folio.scope, copy)}
            strongAuth={strongAuth}
            pendingId={pendingId}
            isError={settleFailed}
            onDecide={onDecide}
          />

          <Link className={styles.back} to="/gate" state={FOLD_STATE}>
            {copy.folioBack}
            <span className={styles.key}>Esc</span>
          </Link>
          <p className={styles.keyHint}>{copy.folioKeyHint}</p>
        </div>
      </section>
    </Sheet>
  )
}

/**
 * 变化那一行。
 *
 * 读操作说明它不改变任何东西；写操作逐字段列出要写入的值。查勘查到旧值时
 * 摆出「原值 → 新值」的对照，没查到时照实说当前值尚未查询 ——
 * 只摆出新值而不说这一点，用户会以为这就是「原来的样子」。
 */
function Change({ change, isPreviewed }: { change: readonly ChangeField[]; isPreviewed: boolean }) {
  const copy = useCopy()

  if (change.length === 0) {
    return <Field label={copy.folioFieldChange} value={copy.folioChangeNone} />
  }

  return (
    <div className={styles.field}>
      <dt className={styles.label}>{copy.folioFieldChange}</dt>
      <dd className={styles.value}>
        {change.map((item) => (
          <span key={item.field} className={styles.changeRow}>
            <span className={styles.changeField}>{item.field}</span>
            {isPreviewed && (
              <span className={styles.changeBefore}>
                {item.before === '' ? copy.folioChangeAbsent : item.before}
                <span aria-hidden="true"> → </span>
              </span>
            )}
            <span className={styles.changeValue}>{item.value}</span>
          </span>
        ))}
        <span className={styles.baseline}>
          {isPreviewed ? copy.folioChangePreviewed : copy.folioChangeBaseline}
        </span>
      </dd>
    </div>
  )
}

function Field({
  label,
  value,
  isMono = false,
  isAccent = false,
}: {
  label: string
  value: string
  isMono?: boolean
  isAccent?: boolean
}) {
  return (
    <div className={styles.field}>
      <dt className={styles.label}>{label}</dt>
      <dd className={cx(styles.value, isMono && styles.mono, isAccent && styles.accent)}>{value}</dd>
    </div>
  )
}

/** 加载态。占位与真实两页同宽同高，卷宗展开的位置不会在数据到达时跳一下。 */
function SheetSkeleton() {
  const copy = useCopy()

  return (
    <>
      <section className={styles.arrival}>
        <p className={styles.loading}>{copy.folioLoading}</p>
        {[0, 1, 2, 3, 4].map((slot) => (
          <span key={slot} className={styles.skeletonRow} />
        ))}
      </section>
      <div className={styles.spine} aria-hidden="true" />
      <section className={styles.consequence}>
        {[0, 1, 2].map((slot) => (
          <span key={slot} className={styles.skeletonRow} />
        ))}
      </section>
    </>
  )
}

/**
 * 错误态与「这条请求不在了」。
 *
 * 两者都不是灾难：缝仍在，既有的授权也没有变。这里给的是下一步，不是堆栈。
 */
function Notice({ title, blurb }: { title: string; blurb: string }) {
  const copy = useCopy()

  return (
    <Sheet>
      <section className={styles.noticeBody} role="status">
        <p className={styles.noticeTitle}>{title}</p>
        <p className={styles.noticeBlurb}>{blurb}</p>
        <Link className={styles.back} to="/gate" state={FOLD_STATE}>
          {copy.backToGate}
        </Link>
      </section>
    </Sheet>
  )
}
