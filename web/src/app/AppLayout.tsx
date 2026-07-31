import { useEffect, useState } from 'react'
import { Outlet, useLocation } from 'react-router'

import { GatewayNotice } from '../boundary/GatewayNotice'
import { CommandPalette } from '../components/CommandPalette'
import { useGatewayStatus } from '../data/gatewayStatus'
import { useLiveUpdates } from '../data/liveUpdates'
import { pendingCountOf, usePassages } from '../data/passages'
import { useCopy } from '../i18n/copy'
import { useLanguageStore } from '../state/language'
import { useResolvedTheme } from '../state/theme'

import styles from './AppLayout.module.css'
import { BoundaryBar } from './BoundaryBar'
import { boundaryStateOf, useDocumentIdentity } from './documentIdentity'
import { pageTitleOf } from './routes'

/**
 * 所有页面共用的外壳：顶部边界栏 + 当前页面。
 *
 * Gateway 的离线说明在这一层而不是在 Gate 里：连不上是整个 Console 的处境，
 * 不是某一页的处境（REQ-GATEWAY-003）。
 */
export function AppLayout() {
  const copy = useCopy()
  const language = useLanguageStore((state) => state.language)
  const theme = useResolvedTheme()
  const { pathname } = useLocation()
  const { state, status } = useGatewayStatus()
  const { passages } = usePassages()
  const pending = pendingCountOf(passages)

  // 订阅放在外壳而不是 Gate：标签标题与顶栏角标在每一页都要跟着变。
  useLiveUpdates()

  const [isCommandsOpen, setCommandsOpen] = useState(false)
  // ⌘K 在每一页都要叫得出来，所以挂在 window 上而不是某个容器上。
  // Ctrl+K 一并认：这台机器不一定是 Mac。
  useEffect(() => {
    const handle = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== 'k' || !(event.metaKey || event.ctrlKey)) {
        return
      }
      event.preventDefault()
      setCommandsOpen(true)
    }
    window.addEventListener('keydown', handle)
    return () => {
      window.removeEventListener('keydown', handle)
    }
  }, [])

  useDocumentIdentity({
    page: pageTitleOf(pathname, copy),
    pending,
    state: boundaryStateOf(state, pending),
    language,
    copy,
    theme,
  })

  return (
    <div className={styles.app}>
      <BoundaryBar
        connection={state}
        status={status}
        pending={pending}
        onOpenCommands={() => {
          setCommandsOpen(true)
        }}
      />
      <main className={styles.body}>
        <Outlet />
        <GatewayNotice state={state} />
      </main>

      {/* 面板挂在外壳上：每一页都要能用 ⌘K 叫出来（REQ-UI-011）。 */}
      <CommandPalette
        isOpen={isCommandsOpen}
        onClose={() => {
          setCommandsOpen(false)
        }}
      />
    </div>
  )
}
