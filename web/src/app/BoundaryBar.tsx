import { useRef, useState, type ReactNode } from 'react'
import { NavLink, useNavigate } from 'react-router'

import { cx } from '../components/cx'
import { useIsCompact, useIsNarrow } from '../components/viewport'
import type { ConnectionState, GatewayStatus } from '../data/gatewayStatus'
import { useCopy, type Copy } from '../i18n/copy'
import { nextLanguage, useLanguageStore } from '../state/language'
import { NEXT_PREFERENCE, useThemeStore, type ThemePreference } from '../state/theme'

import styles from './BoundaryBar.module.css'

/**
 * 导航项。Gate / Identities / Automation / Ledger 在设计稿的中文界面里就是英文，
 * 它们是这个产品里的专名，不随语言变化。
 *
 * `onNarrow` 是小于 1024 时还留下的那两项：PRD §24 在那个宽度上只提供审批、
 * Lease、History 与紧急撤销 —— 前三样在 Gate 与 Ledger 上。
 */
const NAV_ITEMS = [
  { label: 'Gate', to: '/gate', icon: <GateIcon />, onNarrow: true },
  { label: 'Identities', to: '/identities', icon: <IdentitiesIcon />, onNarrow: false },
  { label: 'Automation', to: '/automation', icon: <AutomationIcon />, onNarrow: false },
  { label: 'Ledger', to: '/ledger', icon: <LedgerIcon />, onNarrow: true },
] as const

function BrandMark() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M6.2 1.8v4.4M6.2 9.8v4.4M9.8 1.8v4.4M9.8 9.8v4.4"
        stroke="var(--text)"
        strokeWidth="1.4"
        strokeLinecap="round"
      />
      <path d="M1.2 8h3.4M11.4 8h3.4" stroke="var(--human)" strokeWidth="1.4" strokeLinecap="round" />
      <circle cx="8" cy="8" r="1.15" fill="var(--human)" />
    </svg>
  )
}

/* 四个导航图标取自设计稿 §03 的 1024 顶栏，路径原样搬过来。 */

function NavIcon({ children }: { readonly children: ReactNode }) {
  return (
    <svg className={styles.navIcon} width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      {children}
    </svg>
  )
}

function GateIcon() {
  return (
    <NavIcon>
      <path
        d="M6.5 2v4.5M6.5 9.5V14M9.5 2v4.5M9.5 9.5V14"
        stroke="currentColor"
        strokeWidth="1.3"
        strokeLinecap="round"
      />
      <circle cx="8" cy="8" r="1" fill="currentColor" />
    </NavIcon>
  )
}

function IdentitiesIcon() {
  return (
    <NavIcon>
      <path d="M6.5 3.5H3v9h3.5" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <rect x="8.5" y="4.5" width="5" height="7" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
    </NavIcon>
  )
}

function AutomationIcon() {
  return (
    <NavIcon>
      <path d="M3 4.5h10M3 11.5h10" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <path d="M3 8h3.5M9.5 8H13" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <circle cx="8" cy="8" r="1.4" stroke="currentColor" strokeWidth="1.2" />
    </NavIcon>
  )
}

function LedgerIcon() {
  return (
    <NavIcon>
      <path d="M8 2v12" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <circle cx="11.5" cy="8" r="1.3" fill="currentColor" />
      <path d="M8 8h2" stroke="currentColor" strokeWidth="1.1" />
    </NavIcon>
  )
}

function ThemeIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <path
        d="M11.4 8.6A5 5 0 0 1 5.4 2.6a4.6 4.6 0 1 0 6 6Z"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinecap="round"
      />
    </svg>
  )
}

interface MenuItem {
  readonly label: string
  /** 右侧的一小段说明：当前语言、当前主题、快捷键。没有就不画。 */
  readonly hint: string
  readonly run: () => void
}

/**
 * 溢出菜单。
 *
 * Preferences 始终只从这里进入（REQ-UI-001）；1280 以下语言与主题也收进来
 * （REQ-UI-004 ④）。两处用的是同一份条目，顶栏不存在第二套开关。
 */
function OverflowMenu({ label, items }: { readonly label: string; readonly items: readonly MenuItem[] }) {
  const [isOpen, setIsOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)

  function close() {
    setIsOpen(false)
    trigger.current?.focus()
  }

  return (
    <div
      className={styles.more}
      onKeyDown={(event) => {
        // Esc 收回并把焦点还给触发它的按钮。
        if (event.key === 'Escape' && isOpen) {
          close()
        }
      }}
    >
      <button
        ref={trigger}
        type="button"
        className={cx(styles.iconButton, isOpen && styles.iconButtonOpen)}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        onClick={() => {
          setIsOpen(!isOpen)
        }}
      >
        ⋯
      </button>

      {isOpen && (
        <div className={styles.menu} role="menu" aria-label={label}>
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className={styles.menuItem}
              onClick={() => {
                item.run()
                close()
              }}
            >
              <span className={styles.menuLabel}>{item.label}</span>
              {item.hint !== '' && <span className={styles.menuHint}>{item.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

export interface BoundaryBarProps {
  readonly connection: ConnectionState
  /** 最近一次成功读到的状态；从未成功过时为 null。 */
  readonly status: GatewayStatus | null
  /** 待审批数量，0 时不显示角标。 */
  readonly pending: number
  /** 唤出命令面板（REQ-UI-011）。面板本身挂在外壳上，顶栏只负责叫它。 */
  readonly onOpenCommands: () => void
}

/**
 * 顶部边界栏。Gateway 选择器居中，因为它决定这条缝属于谁
 */
export function BoundaryBar({ connection, status, pending, onOpenCommands }: BoundaryBarProps) {
  const copy = useCopy()
  const navigate = useNavigate()
  const isCompact = useIsCompact()
  const isNarrow = useIsNarrow()
  const language = useLanguageStore((state) => state.language)
  const setLanguage = useLanguageStore((state) => state.setLanguage)
  const preference = useThemeStore((state) => state.preference)
  const setPreference = useThemeStore((state) => state.setPreference)

  const connectionLabel: Record<ConnectionState, string> = {
    connecting: copy.connectionConnecting,
    connected: copy.connectionConnected,
    disconnected: copy.connectionDisconnected,
  }
  const preferenceLabel: Record<ThemePreference, string> = {
    system: copy.themeSystem,
    dark: copy.themeDark,
    light: copy.themeLight,
  }

  function switchLanguage() {
    setLanguage(nextLanguage(language))
  }

  function switchTheme() {
    setPreference(NEXT_PREFERENCE[preference])
  }

  const menuItems: MenuItem[] = [
    // 1280 以下顶栏没有 ⌘K 按钮，入口收进菜单 —— 触屏上按不出这个快捷键。
    ...(isCompact ? [{ label: copy.commandLabel, hint: '⌘K', run: onOpenCommands }] : []),
    ...(isCompact ? compactOnlyItems(copy, preferenceLabel[preference], switchLanguage, switchTheme) : []),
    {
      label: copy.morePreferences,
      hint: '',
      run: () => {
        void navigate('/preferences')
      },
    },
  ]

  // 连不上时仍然显示上一次已知的地址：这道缝属于谁没有变，只是暂时没人应答。
  const host = status === null ? '127.0.0.1:8787' : `${status.listen_address}:${String(status.web_api_port)}`

  return (
    <header className={styles.bar}>
      <div className={styles.brand}>
        <BrandMark />
        <span className={styles.brandName}>OpenDelo</span>
      </div>

      <nav className={styles.nav} aria-label={copy.navLabel}>
        {NAV_ITEMS.filter((item) => item.onNarrow || !isNarrow).map(({ label, to, icon }) => (
          <NavLink
            key={to}
            to={to}
            // tooltip 只在图标形态上给：名字看得见的时候再挂一个同样的提示是噪音。
            title={isCompact ? label : undefined}
            className={({ isActive }) => cx(styles.navItem, isActive && styles.navItemActive)}
          >
            {icon}
            <span className={styles.navLabel}>{label}</span>
            {to === '/gate' && pending > 0 && (
              <span className={styles.pending} aria-label={copy.pendingBadgeAria(pending)}>
                <span className={styles.pendingCount}>{pending}</span>
              </span>
            )}
          </NavLink>
        ))}
      </nav>

      <div className={styles.gatewaySlot}>
        <div className={styles.gateway}>
          <span className={styles.gatewaySeam} aria-hidden="true" />
          <span>{copy.gatewayDevice}</span>
          <span className={styles.gatewayHost}>{host}</span>
          <span
            className={cx(
              styles.gatewayDot,
              connection === 'connected' && styles.gatewayDotConnected,
              connection === 'connecting' && styles.gatewayDotConnecting,
              connection === 'disconnected' && styles.gatewayDotDisconnected,
            )}
            aria-hidden="true"
          />
          <span className={styles.gatewayState}>{connectionLabel[connection]}</span>
        </div>
      </div>

      <div className={styles.controls}>
        {!isCompact && (
          <>
            <button
              type="button"
              className={styles.control}
              aria-label={copy.commandAria}
              onClick={onOpenCommands}
            >
              ⌘K<span className={styles.controlHint}>{copy.commandHint}</span>
            </button>
            <button type="button" className={styles.control} onClick={switchLanguage} aria-label={copy.languageAria}>
              {copy.languageToggle}
            </button>
            <button
              type="button"
              className={styles.iconButton}
              onClick={switchTheme}
              aria-label={copy.themeAria(preferenceLabel[preference])}
            >
              <ThemeIcon />
            </button>
          </>
        )}
        <OverflowMenu label={copy.moreAria} items={menuItems} />
        <span className={styles.avatar}>YZ</span>
      </div>
    </header>
  )
}

/** 1280 以下从顶栏收进菜单的那三项（REQ-UI-004 ④）。 */
function compactOnlyItems(
  copy: Copy,
  themeLabel: string,
  switchLanguage: () => void,
  switchTheme: () => void,
): MenuItem[] {
  return [
    { label: copy.moreLanguage, hint: copy.languageToggle, run: switchLanguage },
    { label: copy.moreTheme, hint: themeLabel, run: switchTheme },
  ]
}
