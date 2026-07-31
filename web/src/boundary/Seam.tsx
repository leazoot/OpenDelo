import { cx } from '../components/cx'

import styles from './Seam.module.css'

export interface SeamProps {
  /**
   * Gateway 未连接。缝改用 --seam-off 与 offPulse，**位置与宽度不变** ——
   * 缝是空间常量，离线不是让它挪动的理由（REQ-UI-002）。
   */
  readonly offline?: boolean
  /**
   * 最近一次穿越的标识。它变化时凭据核心跳一次（设计稿的 corePulse）。
   *
   * 传标识而不是布尔量：核心靠重新挂载来重播动画，而「跳一下」这件事没有
   * 「结束」可言 —— 用布尔量就得再安排一个把它复位的定时器。
   */
  readonly pulseKey?: string
}

/**
 * Boundary Seam —— 本产品的空间常量。
 *
 * 必须放在一个 `position: relative` 的容器里，缝心恒等于该容器的水平中心。
 * 收窄断点、展开 Inspector 抽屉、加载态都不得改变缝的位置，只能压缩两侧列宽
 * （REQ-UI-002）。缝以外的地方不得重复实现这段几何。
 */
export function Seam({ offline = false, pulseKey = '' }: SeamProps) {
  return (
    <>
      <div className={cx(styles.glow, offline && styles.glowOff)} aria-hidden="true" />
      <div className={styles.seam} aria-hidden="true">
        <div className={cx(styles.fill, offline && styles.fillOff)} />
        <div className={styles.inner} />
        <div className={styles.ticks} />
        {pulseKey !== '' && <div key={pulseKey} className={styles.core} />}
      </div>
    </>
  )
}
