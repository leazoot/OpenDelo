import { Link } from 'react-router'

import { cx } from '../components/cx'
import { useLedgerSummary, type LedgerEvent } from '../data/ledgerSummary'
import { useCopy } from '../i18n/copy'

import { clockTimeOf } from './ledgerText'

import styles from './LedgerRibbon.module.css'

/*
 * Gate 底部的账本条（设计稿 §01）。
 *
 * 只有今天的次数与最近几条，没有图表也没有过滤器 —— 那些是 Boundary Ledger
 * 那一页的事。
 */

/** 结论 → 圆点的颜色。认不出的结论用中性色，不硬塞进「通行」或「拒绝」。 */
function toneOf(event: LedgerEvent): string | undefined {
  if (event.verdict === 'allow') {
    return styles.dotPassed
  }
  if (event.verdict === 'deny') {
    return styles.dotRefused
  }
  return undefined
}

export function LedgerRibbon() {
  const copy = useCopy()
  const { summary, isLoading, isError } = useLedgerSummary()

  return (
    <section className={styles.ribbon} aria-label={copy.ledgerRibbonLabel}>
      <div className={styles.heading}>
        <h2 className={styles.title}>{copy.ledgerRibbonTitle}</h2>
        <span className={styles.counts}>
          {isError
            ? copy.ledgerUnavailable
            : copy.ledgerCounts(summary.passed, summary.refused, summary.isPartial)}
        </span>
        <Link className={styles.all} to="/ledger">
          {copy.ledgerSeeAll}
        </Link>
      </div>

      {isLoading || summary.recent.length === 0 ? (
        <p className={styles.quiet}>{isLoading ? copy.ledgerLoading : copy.ledgerEmpty}</p>
      ) : (
        <ul className={styles.entries}>
          {summary.recent.map((event) => (
            <li key={event.id} className={styles.entry}>
              <span className={styles.stamp}>
                <span className={cx(styles.dot, toneOf(event))} aria-hidden="true" />
                <span className={styles.time}>{clockTimeOf(event.created_at)}</span>
              </span>
              <span className={styles.text}>
                {event.service} · {event.type}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
