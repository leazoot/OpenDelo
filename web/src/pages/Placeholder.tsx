import type { ReactNode } from 'react'

import styles from './Placeholder.module.css'

/*
 * 页面骨架：说明这一页是什么。
 *
 * 语气是说明性的，而不是「暂无数据」。
 */

export interface PlaceholderProps {
  readonly title: string
  readonly blurb: string
  readonly children?: ReactNode
}

export function Placeholder({ title, blurb, children }: PlaceholderProps) {
  return (
    <section className={styles.page}>
      <h1 className={styles.title}>{title}</h1>
      <p className={styles.blurb}>{blurb}</p>
      {children}
    </section>
  )
}
