import { Link } from 'react-router'

import { cx } from '../components/cx'
import { useAgentStats } from '../data/agentStats'
import { isActionOffered, useSettleApproval, type DecisionAction } from '../data/decisions'
import type { GatewayStatus } from '../data/gatewayStatus'
import type { Passage } from '../data/passages'
import { useCopy } from '../i18n/copy'

import styles from './Inspector.module.css'
import { mayDecideHere } from './inspectorRules'
import { reasonTextOf, riskTextOf } from './passageText'

/*
 * Inspector：被选中的那一条请求（设计稿 §01、REQ-UI-003）。
 *
 * `Credential` 一行恒为「留在缝内」。这不是一个占位文案 —— 这一行在结构上
 * 就没有能装凭据的字段可用，界面拿不到，也就展示不出来（REQ-UI-003 AC2）。
 */

export interface InspectorProps {
  readonly passage: Passage | null
  readonly agentName: string | undefined
  readonly status: GatewayStatus | null
  /** 抽屉是否展开。≥1280 恒为常驻，这个值只影响 1024 以下的形态。 */
  readonly isDrawerOpen: boolean
}

export function Inspector({ passage, agentName, status, isDrawerOpen }: InspectorProps) {
  const copy = useCopy()

  return (
    <aside
      className={cx(styles.inspector, isDrawerOpen && styles.drawerOpen)}
      aria-label={copy.inspectorLabel}
    >
      {passage === null ? (
        <div className={styles.head}>
          <h2 className={styles.title}>{copy.inspectorTitle}</h2>
          <p className={styles.empty}>{copy.inspectorEmpty}</p>
        </div>
      ) : (
        <Selected passage={passage} agentName={agentName} status={status} />
      )}
    </aside>
  )
}

function Selected({
  passage,
  agentName,
  status,
}: {
  passage: Passage
  agentName: string | undefined
  status: GatewayStatus | null
}) {
  const copy = useCopy()
  const stats = useAgentStats(passage.agentId)
  const { settle, pendingId, isError } = useSettleApproval()

  const decidable = mayDecideHere(passage)
  const host = status === null ? '' : `${status.listen_address}:${String(status.web_api_port)}`
  const submitting = pendingId === passage.approvalId && pendingId !== ''

  const decide = (action: DecisionAction) => {
    settle({ approvalId: passage.approvalId, action })
  }

  return (
    <>
      <div className={styles.head}>
        <h2 className={styles.title}>{copy.inspectorTitle}</h2>
        <p className={styles.subject}>
          {agentName ?? passage.agentId} → {passage.service}
        </p>
        <p className={styles.detail}>
          {passage.operation} · {passage.resource}
        </p>
      </div>

      <dl className={styles.rows}>
        <Row label={copy.inspectorIdentity} value={agentName ?? passage.agentId} />
        <Row label={copy.inspectorGateway} value={host} isMono />
        <Row label={copy.inspectorRule} value={reasonTextOf(passage.reason, copy)} isAccent />
        {/* 恒定的一行。凭据从不越过缝，界面上也就没有它的位置。 */}
        <Row label={copy.inspectorCredential} value={copy.inspectorCredentialValue} />
      </dl>

      <div className={styles.stats}>
        <h3 className={styles.statsTitle}>{copy.inspectorStatsTitle}</h3>
        <ul className={styles.statList}>
          <Stat
            value={copy.countWithFloor(stats.passed, stats.isPartial)}
            label={copy.inspectorPassed}
            tone={styles.passed}
          />
          <Stat
            value={copy.countWithFloor(stats.confirmed, stats.isPartial)}
            label={copy.inspectorConfirmed}
            tone={styles.confirmed}
          />
          <Stat
            value={copy.countWithFloor(stats.refused, stats.isPartial)}
            label={copy.inspectorRefused}
            tone={styles.refused}
          />
        </ul>
      </div>

      <div className={styles.actions}>
        <p className={styles.risk}>{riskTextOf(passage.riskLevel, copy)}</p>

        <Link className={styles.folio} to={`/gate/folio/${passage.id}`}>
          {copy.inspectorOpenFolio}
          <span className={styles.key}>↵</span>
        </Link>

        {passage.riskLevel === 'high' && <p className={styles.note}>{copy.inspectorHighRisk}</p>}
        {passage.verdict !== 'waiting' && <p className={styles.note}>{copy.inspectorDecided}</p>}
        {isError && (
          <p className={styles.note} role="status">
            {copy.inspectorFailed}
          </p>
        )}

        <div className={styles.decide}>
          <button
            type="button"
            className={cx(styles.allow)}
            disabled={!decidable || submitting || !isActionOffered(passage.availableActions, 'allow-task')}
            onClick={() => {
              decide('allow-task')
            }}
          >
            {copy.inspectorAllow}
            <span className={styles.key}>A</span>
          </button>
          <button
            type="button"
            className={cx(styles.deny)}
            disabled={!decidable || submitting || !isActionOffered(passage.availableActions, 'deny')}
            onClick={() => {
              decide('deny')
            }}
          >
            {copy.inspectorDeny}
            <span className={styles.key}>D</span>
          </button>
        </div>

        <button
          type="button"
          className={styles.once}
          disabled={!decidable || submitting || !isActionOffered(passage.availableActions, 'allow-once')}
          onClick={() => {
            decide('allow-once')
          }}
        >
          {copy.inspectorAllowOnce}
          <span className={styles.key}>⇧A</span>
        </button>

        {/*
          第五种操作（PRD §13.2）。它只在后端提供时出现 —— 高风险的
          available_actions 里没有它，因为那时学不成任何记忆。
        */}
        {isActionOffered(passage.availableActions, 'always-ask') && (
          <button
            type="button"
            className={styles.once}
            disabled={!decidable || submitting}
            onClick={() => {
              decide('always-ask')
            }}
          >
            {copy.inspectorAlwaysAsk}
          </button>
        )}
      </div>
    </>
  )
}

function Row({
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
    <div className={styles.row}>
      <dt className={styles.rowLabel}>{label}</dt>
      <dd className={cx(styles.rowValue, isMono && styles.mono, isAccent && styles.accent)}>{value}</dd>
    </div>
  )
}

function Stat({ value, label, tone }: { value: string; label: string; tone: string | undefined }) {
  return (
    <li className={styles.stat}>
      <span className={cx(styles.statValue, tone)}>{value}</span>
      {label}
    </li>
  )
}
