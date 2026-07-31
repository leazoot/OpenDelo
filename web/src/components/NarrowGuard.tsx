import type { ReactNode } from 'react'
import { Link } from 'react-router'

import { useCopy, type Copy } from '../i18n/copy'

import styles from './NarrowGuard.module.css'
import { useIsNarrow } from './viewport'

/*
 * 窄屏拦截（PRD §24「小于 1024 只提供审批、Lease、History、紧急撤销」，
 * REQ-UI-004 AC3 要求路由级拦截）。
 *
 * 拦截而不是用 CSS 隐藏：改一条策略要看得见完整的上下文（影响到谁、
 * 现在按什么规则放行）。把控件挤进 480px 的屏幕里，用户会在看不全后果的
 * 情况下改掉一条规则 —— 那比不给他改更糟。
 */

/** 被拦下的三类事。拦截页要让人知道自己少了什么，而不只是「太窄了」。 */
export type GuardedArea = 'preferences' | 'identities' | 'automation'

const TITLES: Record<GuardedArea, (copy: Copy) => string> = {
  preferences: (copy) => copy.narrowTitlePreferences,
  identities: (copy) => copy.narrowTitleIdentities,
  automation: (copy) => copy.narrowTitleAutomation,
}

export interface NarrowGuardProps {
  readonly area: GuardedArea
  readonly children: ReactNode
}

export function NarrowGuard({ area, children }: NarrowGuardProps) {
  const copy = useCopy()

  if (!useIsNarrow()) {
    return children
  }
  return (
    <div className={styles.guard} role="status">
      <strong className={styles.title}>{TITLES[area](copy)}</strong>
      <p className={styles.blurb}>{copy.narrowBlurb}</p>
      <Link className={styles.back} to="/gate">
        {copy.narrowBack}
      </Link>
    </div>
  )
}
