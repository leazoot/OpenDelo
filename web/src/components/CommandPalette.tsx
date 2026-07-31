import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router'

import { useLeases, useRevokeLease } from '../data/leases'
import { usePassages } from '../data/passages'
import { useCopy, type Copy } from '../i18n/copy'
import { nextLanguage, useLanguageStore } from '../state/language'
import { NEXT_PREFERENCE, useThemeStore } from '../state/theme'

import styles from './CommandPalette.module.css'
import { cx } from './cx'
import { useIsNarrow } from './viewport'

/*
 * ⌘K 命令面板（REQ-UI-011、设计稿 §01 的 `⌘K 命令`）。
 *
 * **每一项都通向一条已经存在的路由或一个已经存在的端点。** 这不是一个能力入口
 * 的集散地：面板里出现一件别处做不到的事，就等于从这里悄悄扩了业务范围
 * （假设 A-10 只把它当作同一能力的另一个入口）。
 *
 * 设计稿只画了顶栏上的那个触发器，没有画面板本身；这里按设计语言补全
 */

export interface Command {
  readonly id: string
  readonly group: string
  readonly label: string
  /** 右侧的一小段说明：快捷键、剩余时间、当前取值。 */
  readonly hint: string
  /** 破坏性动作在面板里也要问一次，与缝内侧那一架用的是同一条规矩。 */
  readonly confirm?: string
  readonly run: () => void
}

export interface CommandPaletteProps {
  readonly isOpen: boolean
  readonly onClose: () => void
}

/**
 * 关着的时候整块不挂载。
 *
 * 这样输入的内容、光标位置、确认到一半的那一步都随关闭消失，不必在打开时
 * 一项项重置 —— 重置漏掉一项，下次打开就会带着上次的残留。
 */
export function CommandPalette({ isOpen, onClose }: CommandPaletteProps) {
  return isOpen ? <Panel onClose={onClose} /> : null
}

function Panel({ onClose }: { readonly onClose: () => void }) {
  const copy = useCopy()
  const commands = useCommands(copy, onClose)

  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const [confirming, setConfirming] = useState<Command | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const opener = useRef<Element | null>(null)
  const listId = useId()

  const matches = useMemo(() => filter(commands, query), [commands, query])
  const active = matches[Math.min(cursor, matches.length - 1)] ?? null

  useEffect(() => {
    // 记住是谁把它叫出来的，Esc 之后焦点原样还回去（REQ-UI-011 AC2）。
    opener.current = document.activeElement
    input.current?.focus()
  }, [])

  function close() {
    onClose()
    if (opener.current instanceof HTMLElement) {
      opener.current.focus()
    }
  }

  function choose(command: Command | null) {
    if (command === null) {
      return
    }
    if (command.confirm !== undefined && confirming?.id !== command.id) {
      setConfirming(command)
      return
    }
    command.run()
    close()
  }

  function onKeyDown(event: React.KeyboardEvent) {
    // 面板开着的时候，缝前的快捷键不该同时生效 —— 那会一边关面板一边清掉选中。
    event.stopPropagation()

    if (event.key === 'Escape') {
      // 确认态先退回列表：Esc 撤销的是刚才那一步，不是整个面板。
      if (confirming !== null) {
        setConfirming(null)
        return
      }
      close()
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      choose(confirming ?? active)
      return
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (matches.length === 0) {
        return
      }
      const step = event.key === 'ArrowDown' ? 1 : -1
      setConfirming(null)
      setCursor((current) => (current + step + matches.length) % matches.length)
    }
  }

  return (
    <div className={styles.layer} onKeyDown={onKeyDown}>
      <button
        type="button"
        className={styles.scrim}
        aria-label={copy.commandClose}
        onClick={() => {
          close()
        }}
      />

      <div className={styles.panel} role="dialog" aria-modal="true" aria-label={copy.commandLabel}>
        <input
          ref={input}
          className={styles.query}
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-activedescendant={active === null ? undefined : `${listId}-${active.id}`}
          aria-label={copy.commandLabel}
          placeholder={copy.commandPlaceholder}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value)
            setCursor(0)
            setConfirming(null)
          }}
        />

        {confirming === null ? (
          <ul className={styles.list} id={listId} role="listbox" aria-label={copy.commandLabel}>
            {matches.length === 0 && <li className={styles.empty}>{copy.commandEmpty}</li>}
            {matches.map((command) => (
              <li
                key={command.id}
                id={`${listId}-${command.id}`}
                role="option"
                aria-selected={command.id === active?.id}
                className={cx(styles.item, command.id === active?.id && styles.itemActive)}
                onClick={() => {
                  choose(command)
                }}
              >
                <span className={styles.group}>{command.group}</span>
                <span className={styles.label}>{command.label}</span>
                {command.hint !== '' && <span className={styles.hint}>{command.hint}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className={styles.confirm} role="alert">
            {confirming.confirm}
            <span className={styles.hint}>{copy.commandConfirmKeys}</span>
          </p>
        )}

        <p className={styles.footer}>{copy.commandFooter}</p>
      </div>
    </div>
  )
}

function filter(commands: readonly Command[], query: string): readonly Command[] {
  const needle = query.trim().toLowerCase()
  if (needle === '') {
    return commands
  }
  return commands.filter((command) => `${command.group} ${command.label}`.toLowerCase().includes(needle))
}

/**
 * 面板上的全部条目。
 *
 * 页面跳转、待审批、收回授权、语言与主题 —— 四类，每一类都指向界面上本来就
 * 做得到的事。**没有「切换 Gateway」**：本期只连本机一条缝，多 Gateway 的能力
 * 本期根本不存在，给它一个入口就是在假装它已经有了。
 */
function useCommands(copy: Copy, onClose: () => void): readonly Command[] {
  const navigate = useNavigate()
  const isNarrow = useIsNarrow()
  const { passages } = usePassages()
  const { leases } = useLeases()
  const { revoke } = useRevokeLease()
  const language = useLanguageStore((state) => state.language)
  const setLanguage = useLanguageStore((state) => state.setLanguage)
  const preference = useThemeStore((state) => state.preference)
  const setPreference = useThemeStore((state) => state.setPreference)

  const go = (path: string) => () => {
    void navigate(path)
  }

  // 窄屏拦下的三块在这里也不出现：面板给的是入口，不是绕过拦截的后门。
  const pages: Command[] = [
    { id: 'page-gate', group: copy.commandGroupPages, label: copy.pageGate, hint: '', run: go('/gate') },
    { id: 'page-ledger', group: copy.commandGroupPages, label: copy.pageLedger, hint: '', run: go('/ledger') },
    ...(isNarrow
      ? []
      : [
          {
            id: 'page-identities',
            group: copy.commandGroupPages,
            label: copy.pageIdentities,
            hint: '',
            run: go('/identities'),
          },
          {
            id: 'page-automation',
            group: copy.commandGroupPages,
            label: copy.pageAutomation,
            hint: '',
            run: go('/automation'),
          },
          {
            id: 'page-preferences',
            group: copy.commandGroupPages,
            label: copy.pagePreferences,
            hint: '',
            run: go('/preferences'),
          },
        ]),
  ]

  const pending: Command[] = passages
    .filter((passage) => passage.verdict === 'waiting')
    .map((passage) => ({
      id: `folio-${passage.id}`,
      group: copy.commandGroupPending,
      label: copy.commandOpenFolio(`${passage.operation} · ${passage.resource}`),
      hint: passage.service,
      run: go(`/gate/folio/${passage.id}`),
    }))

  const grants: Command[] = leases.map((lease) => ({
    id: `lease-${lease.id}`,
    group: copy.commandGroupLeases,
    label: copy.commandRevokeLease(lease.service),
    hint: lease.scope,
    confirm: copy.commandRevokeConfirm(lease.service),
    run: () => {
      revoke(lease.id)
    },
  }))

  const shell: Command[] = [
    {
      id: 'switch-language',
      group: copy.commandGroupConsole,
      label: copy.languageAria,
      hint: copy.languageToggle,
      run: () => {
        setLanguage(nextLanguage(language))
      },
    },
    {
      id: 'switch-theme',
      group: copy.commandGroupConsole,
      label: copy.commandSwitchTheme,
      hint: '',
      run: () => {
        setPreference(NEXT_PREFERENCE[preference])
      },
    },
    { id: 'close', group: copy.commandGroupConsole, label: copy.commandClose, hint: 'Esc', run: onClose },
  ]

  return [...pending, ...pages, ...grants, ...shell]
}
