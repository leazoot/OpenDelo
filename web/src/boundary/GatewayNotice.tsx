import type { ConnectionState } from '../data/gatewayStatus'
import { useCopy, type Copy } from '../i18n/copy'

import styles from './GatewayNotice.module.css'

interface Notice {
  readonly title: string
  readonly body: string
  /** 恢复的办法。命令本身两种语言下是同一个字符串，不进字典。 */
  readonly command?: string
}

/**
 * 三种连接状态各有一段说明。
 *
 * 离线时说清楚三件事：本地数据没丢、没有请求会绕过这道缝、怎么恢复
 * （REQ-GATEWAY-003 AC2）。不用「受限模式」这类说法，也不把它写成一次错误
 * （REQ-UI-012）。
 */
function noticeOf(state: ConnectionState, copy: Copy): Notice | null {
  switch (state) {
    case 'connected':
      return null
    case 'connecting':
      return { title: copy.gatewayConnectingTitle, body: copy.gatewayConnectingBody }
    case 'disconnected':
      return { title: copy.gatewayOfflineTitle, body: copy.gatewayOfflineBody, command: 'opendelo start' }
  }
}

export interface GatewayNoticeProps {
  readonly state: ConnectionState
}

export function GatewayNotice({ state }: GatewayNoticeProps) {
  const copy = useCopy()
  const notice = noticeOf(state, copy)
  if (notice === null) {
    return null
  }

  return (
    <aside className={styles.notice} aria-live="polite">
      <p className={styles.title}>
        <span className={styles.marker} aria-hidden="true" />
        {notice.title}
      </p>
      <p className={styles.body}>{notice.body}</p>
      {notice.command !== undefined && <p className={styles.command}>{notice.command}</p>}
    </aside>
  )
}
