import { cx } from '../components/cx'
import { markOf } from '../data/agents'
import type { Passage as PassageModel, Verdict } from '../data/passages'
import type { Copy } from '../i18n/copy'

import styles from './Passage.module.css'
import { reasonTextOf, riskTextOf, verdictTextOf } from './passageText'

/*
 * 一次穿越：外侧的请求卡片 → 轨迹 → 内侧的目的地与结论（设计稿 §01）。
 *
 * 卡片永远在缝外一侧，目的地永远在缝内一侧。这个方向不随结论改变 ——
 * 被拒绝的请求也是从外面来的。
 */

// CSS Modules 的类型是索引签名，取出来是 `string | undefined`。
const VERDICT_CLASS: Record<Verdict, string | undefined> = {
  waiting: styles.waiting,
  allowed: styles.allowed,
  denied: styles.denied,
  cancelled: styles.cancelled,
}

export interface PassageProps {
  readonly passage: PassageModel
  readonly agentName: string | undefined
  readonly copy: Copy
  readonly isSelected?: boolean
  readonly onSelect?: () => void
}

export function Passage({ passage, agentName, copy, isSelected = false, onSelect }: PassageProps) {
  const name = agentName ?? passage.agentId
  const verdictText = verdictTextOf(passage.verdict, copy)
  const reason = passage.reason === '' ? '' : reasonTextOf(passage.reason, copy)

  return (
    <li className={cx(styles.row, VERDICT_CLASS[passage.verdict])}>
      <div className={styles.outside}>
        <button
          type="button"
          className={cx(styles.card, isSelected && styles.cardSelected)}
          aria-pressed={isSelected}
          onClick={onSelect}
        >
          <span className={styles.mark} aria-hidden="true">
            {markOf(name, passage.agentId)}
          </span>
          <div className={styles.body}>
            <div className={styles.heading}>
              <span className={styles.agent}>{name}</span>
              <span className={styles.risk}>{riskTextOf(passage.riskLevel, copy)}</span>
            </div>
            <p className={styles.action}>
              {passage.operation} · {passage.resource}
            </p>
          </div>
        </button>
      </div>

      <div className={styles.inside}>
        <span className={styles.trace} aria-hidden="true">
          <span className={styles.spark} />
        </span>
        <div className={styles.destination}>
          <span className={styles.service}>{passage.service}</span>
          <span className={styles.verdict}>
            {verdictText}
            {reason === '' ? '' : ` · ${reason}`}
          </span>
        </div>
      </div>
    </li>
  )
}
