import { useState } from 'react'
import { Link } from 'react-router'

import { Seam } from '../../boundary/Seam'
import { cx } from '../../components/cx'
import { markOf, useAgents } from '../../data/agents'
import { useNow } from '../../data/clock'
import { useIdentities } from '../../data/identities'
import { useLeases } from '../../data/leases'
import { useTrustMemories } from '../../data/trustMemories'
import { useCopy } from '../../i18n/copy'

import styles from './IdentitiesPage.module.css'
import {
  agentCards,
  alreadyHolds,
  destinationCards,
  draftOf,
  signPath,
  type AgentCard,
  type DestinationCard,
  type Draft,
} from './relations'

/*
 * Identities —— 双向关系工作台（设计稿 §05、REQ-UI-005）。
 *
 * 左列在缝外，右列在缝内，缝仍在正中且不因两列的多寡而移动（REQ-UI-002）。
 * **拖放只生成草稿**（REQ-IDENT-003 AC1）：这一页不产生任何授权，
 * 它连一个能签发 Lease 的端点都不调用。
 */

export function IdentitiesPage() {
  const copy = useCopy()
  const now = useNow()

  const { agents, isLoading: agentsLoading, isError: agentsFailed } = useAgents()
  const { identities, isLoading: identitiesLoading, isError: identitiesFailed } = useIdentities()
  const { leases } = useLeases()
  const { memories } = useTrustMemories()

  // 拿在手上的那个 Agent。拖放与键盘走同一条路：只有鼠标能做的操作
  // 等于这一页有一半功能对键盘不存在。
  const [held, setHeld] = useState<AgentCard | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [refused, setRefused] = useState('')

  const input = { agents, identities, leases, memories, now, isHere: isSameHost(), copy }
  const left = agentCards(input)
  const right = destinationCards(input)

  const isLoading = agentsLoading || identitiesLoading
  const isError = agentsFailed || identitiesFailed

  function drop(destination: DestinationCard, agent: AgentCard | null) {
    if (agent === null) {
      return
    }
    setHeld(null)
    if (alreadyHolds(destination, agent.id)) {
      // 已经有生效授权的组合不再签一次：那条草稿签下去也只是重复一件已经成立的事。
      setRefused(copy.identitiesAlreadyHolds)
      return
    }
    setRefused('')
    setDraft(draftOf(agent, destination))
  }

  return (
    <div className={styles.page}>
      <header className={styles.bar}>
        <div className={styles.hint}>{copy.identitiesKeyHint}</div>
        {/*
          连接身份要先有一条已登记的凭据引用，而登记它的入口本期只在 Gateway 侧。
          画一个点了没反应的按钮比不画更糟（REQ-UI-007 AC2 的同一条原则）。
        */}
        <button type="button" className={styles.connect} disabled title={copy.identitiesConnectDisabled}>
          {copy.identitiesConnect}
        </button>
      </header>

      <div className={styles.stage}>
        <Seam />

        {isError && (
          <p className={styles.notice} role="status">
            <strong className={styles.noticeTitle}>{copy.identitiesErrorTitle}</strong>
            {copy.identitiesErrorBlurb}
          </p>
        )}

        {!isError && (
          <div className={styles.columns}>
            <section className={styles.column} aria-label={copy.identitiesOutside}>
              <h2 className={styles.eyebrow}>
                {copy.identitiesOutside}
                <span className={styles.count}>{copy.identitiesAgentCount(left.length)}</span>
              </h2>

              {isLoading && <Skeleton />}
              {!isLoading && left.length === 0 && <p className={styles.empty}>{copy.identitiesAgentsEmpty}</p>}

              <ul className={styles.cards}>
                {left.map((agent) => (
                  <li key={agent.id}>
                    <button
                      type="button"
                      className={cx(styles.agent, held?.id === agent.id && styles.held)}
                      draggable
                      aria-pressed={held?.id === agent.id}
                      onDragStart={() => {
                        setHeld(agent)
                      }}
                      onClick={() => {
                        setHeld(held?.id === agent.id ? null : agent)
                      }}
                    >
                      <span className={styles.mark} aria-hidden="true">
                        {markOf(agent.name, agent.id)}
                      </span>
                      <span className={styles.body}>
                        <span className={styles.line}>
                          <span className={styles.name}>{agent.name}</span>
                          <span className={styles.kind}>{agent.kind}</span>
                          <span className={cx(styles.badge, agent.isHere && styles.badgeHere)}>
                            {agent.isHere ? copy.identitiesHere : copy.identitiesElsewhere}
                          </span>
                        </span>
                        <span className={styles.sub}>{copy.identitiesDevice(shortId(agent.deviceId))}</span>
                      </span>
                      <span className={styles.tail}>
                        <span className={styles.leases}>
                          {agent.leaseCount === 0
                            ? copy.identitiesNoLease
                            : copy.identitiesLeaseCount(agent.leaseCount)}
                        </span>
                        <span className={styles.sub}>
                          {agent.lastSeenAt === ''
                            ? copy.identitiesNeverSeen
                            : copy.identitiesLastSeen(agent.lastSeenAt)}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>

            <section className={styles.column} aria-label={copy.identitiesInside}>
              <h2 className={cx(styles.eyebrow, styles.eyebrowInside)}>
                {copy.identitiesInside}
                <span className={styles.count}>{copy.identitiesDestCount(right.length)}</span>
              </h2>

              {isLoading && <Skeleton />}
              {!isLoading && right.length === 0 && <p className={styles.empty}>{copy.identitiesDestsEmpty}</p>}

              <ul className={styles.cards}>
                {right.map((destination) => (
                  <li key={destination.id}>
                    <button
                      type="button"
                      className={cx(styles.dest, held !== null && styles.target)}
                      onDragOver={(event) => {
                        event.preventDefault()
                      }}
                      onDrop={(event) => {
                        event.preventDefault()
                        drop(destination, held)
                      }}
                      onClick={() => {
                        drop(destination, held)
                      }}
                    >
                      <span className={styles.line}>
                        <span className={styles.destName}>{destination.name}</span>
                        <span className={styles.kind}>{destination.kind}</span>
                        {destination.isDefault && <span className={styles.badge}>{copy.identitiesDefault}</span>}
                        <span className={styles.rule}>
                          {destination.rule === '' ? copy.identitiesNoRule : destination.rule}
                        </span>
                      </span>
                      <span className={styles.tabs}>
                        {destination.holders.map((holder) => (
                          <span key={holder.leaseId} className={styles.tab}>
                            <span>{holder.who}</span>
                            <span className={styles.left}>{holder.left}</span>
                          </span>
                        ))}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>

              <p className={styles.drop}>{copy.identitiesDropHint}</p>
            </section>
          </div>
        )}
      </div>

      <footer className={styles.foot} aria-live="polite">
        {held !== null && <p className={styles.held}>{copy.identitiesPickedUp(held.name)}</p>}
        {refused !== '' && <p className={styles.refused}>{refused}</p>}
        {draft !== null && (
          <div className={styles.draft}>
            <span className={styles.eyebrow}>{copy.identitiesDraftTitle}</span>
            <p className={styles.draftLine}>{copy.identitiesDraft(draft.agentName, draft.identityName)}</p>
            <p className={styles.draftBlurb}>{copy.identitiesDraftBlurb}</p>
            <div className={styles.draftActions}>
              <Link className={styles.sign} to={signPath(draft)}>
                {copy.identitiesDraftSign}
              </Link>
              <button
                type="button"
                className={styles.discard}
                onClick={() => {
                  setDraft(null)
                }}
              >
                {copy.identitiesDraftDiscard}
              </button>
            </div>
          </div>
        )}
      </footer>
    </div>
  )
}

/**
 * 请求来自你正坐在的这台机器吗。
 *
 * MVP 的 Agent 只能从回环连上来（假设 A-04），因此「Agent 所在的机器」
 * 就是 Gateway 所在的机器；浏览器与它是否同一台，才是这个标记要回答的问题
 * （设计稿 §05 的注释：同一 Gateway 可能被多台浏览器访问）。
 */
function isSameHost(): boolean {
  const host = globalThis.location.hostname
  return host === 'localhost' || host === '127.0.0.1' || host === '[::1]' || host === '::1'
}

/** 设备主键取后六位：够把两台机器分开，又不必把整串 ULID 摆在卡片上。 */
function shortId(id: string): string {
  return id === '' ? '—' : id.slice(-6)
}

function Skeleton() {
  return (
    <ul className={styles.cards} aria-hidden="true">
      {[0, 1, 2].map((row) => (
        <li key={row} className={styles.skeleton} />
      ))}
    </ul>
  )
}
