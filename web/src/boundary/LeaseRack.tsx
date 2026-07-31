import { useState } from 'react'

import { cx } from '../components/cx'
import { useNow } from '../data/clock'
import { formatRemaining, isEndingSoon, remainingMillis, useLeases, useRevokeLease, type Lease } from '../data/leases'
import { useCopy } from '../i18n/copy'

import styles from './LeaseRack.module.css'

/*
 * 缝内侧的 Lease 架（REQ-LEASE-003、设计稿 §01）。
 *
 * 标签贴着缝的**内侧**：授权生效在缝内，缝外只有请求。
 * 一条也没有时这一架仍然占位，不然缝内侧会在最后一条到期的瞬间塌下去。
 */

export function LeaseRack() {
  const copy = useCopy()
  const now = useNow()
  const { leases, isLoading } = useLeases()
  const { revoke, pendingId } = useRevokeLease()
  const [confirming, setConfirming] = useState<Lease | null>(null)

  return (
    <section className={styles.rack} aria-label={copy.leaseRackLabel}>
      <h2 className={styles.title}>{copy.leaseRackTitle}</h2>

      {isLoading && <p className={styles.quiet}>{copy.leaseLoading}</p>}
      {!isLoading && leases.length === 0 && <p className={styles.quiet}>{copy.leaseEmpty}</p>}

      <ul className={styles.list}>
        {leases.map((lease) => (
          <LeaseTab
            key={lease.id}
            lease={lease}
            now={now}
            isRevoking={pendingId === lease.id}
            onRequestRevoke={() => {
              setConfirming(lease)
            }}
          />
        ))}
      </ul>

      {confirming !== null && (
        <ConfirmRevoke
          lease={confirming}
          onCancel={() => {
            setConfirming(null)
          }}
          onConfirm={() => {
            revoke(confirming.id)
            setConfirming(null)
          }}
        />
      )}
    </section>
  )
}

interface LeaseTabProps {
  readonly lease: Lease
  readonly now: number
  readonly isRevoking: boolean
  readonly onRequestRevoke: () => void
}

/**
 * 一条生效中的授权。
 *
 * 拖出缝外触发收回（设计稿）。同一个动作用键盘也做得到 —— 标签本身是按钮，
 * 回车与拖出走同一条确认（REQ-UI-009 AC1）。
 */
function LeaseTab({ lease, now, isRevoking, onRequestRevoke }: LeaseTabProps) {
  const copy = useCopy()
  const left = remainingMillis(lease.expiresAt, now)
  const soon = isEndingSoon(left)

  return (
    <li>
      <button
        type="button"
        className={cx(styles.tab, soon && styles.tabSoon)}
        draggable
        disabled={isRevoking}
        onDragEnd={onRequestRevoke}
        onClick={onRequestRevoke}
        aria-label={copy.leaseTabAria(lease.service, formatRemaining(left))}
      >
        <span className={styles.service}>{lease.service}</span>
        <span className={styles.scope}>{lease.scope}</span>
        <span className={cx(styles.left, soon && styles.leftSoon)}>{formatRemaining(left)}</span>
      </button>
    </li>
  )
}

/**
 * 收回前的确认。
 *
 * 收回是破坏性的：Agent 手里正在用的授权会当场失效。它必须是一次明确的回答，
 * 而不是一次误拖的副作用。
 */
function ConfirmRevoke({
  lease,
  onCancel,
  onConfirm,
}: {
  lease: Lease
  onCancel: () => void
  onConfirm: () => void
}) {
  const copy = useCopy()

  return (
    <div className={styles.confirm} role="alertdialog" aria-label={copy.leaseRevokeTitle}>
      <p className={styles.confirmTitle}>{copy.leaseRevokeTitle}</p>
      <p className={styles.confirmBlurb}>{copy.leaseRevokeBlurb(lease.service)}</p>
      <div className={styles.confirmActions}>
        <button type="button" className={styles.confirmPrimary} onClick={onConfirm}>
          {copy.leaseRevokeConfirm}
        </button>
        <button type="button" className={styles.confirmCancel} onClick={onCancel}>
          {copy.leaseRevokeCancel}
        </button>
      </div>
    </div>
  )
}
