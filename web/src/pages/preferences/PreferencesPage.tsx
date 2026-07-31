import { useState, type ReactNode } from 'react'

import { cx } from '../../components/cx'
import { usePreferences, useSavePreferences } from '../../data/preferences'
import { useTrustMemories } from '../../data/trustMemories'
import { useCopy, type Copy } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'
import { useThemeStore, type ThemePreference } from '../../state/theme'

import styles from './PreferencesPage.module.css'
import { useClearTrustMemories } from './useClearTrustMemories'
import { VaultSetup } from './VaultSetup'

/*
 * Preferences（PRD §16.5、REQ-UI-007）。
 *
 * 六个模块全部在场。**本期未实现的项可见但禁用并说明**（AC2）——
 * 一个点得动却什么也不做的开关，比一个明说「还没有」的灰条糟得多。
 *
 * 设计稿没有画这一页，样式沿用别处：小标题、行、说明文字，不引入新色板。
 */

const MODES = ['cautious', 'balanced', 'automatic'] as const

export function PreferencesPage() {
  const copy = useCopy()
  const { preferences, isLoading, isError } = usePreferences()
  const { save } = useSavePreferences()
  const { memories } = useTrustMemories()
  const { language, setLanguage } = useLanguageStore()
  const { preference: theme, setPreference: setTheme } = useThemeStore()

  // 切到谨慎模式会让已经学过的记忆全部失效，因此它要一次确认（REQ-DECIDE-003 AC7）。
  const [pendingMode, setPendingMode] = useState('')
  const [confirmingClear, setConfirmingClear] = useState(false)
  const { clear, cleared, isPending: clearing, isError: clearFailed } = useClearTrustMemories()

  if (isError) {
    return (
      <p className={styles.notice} role="status">
        <strong className={styles.noticeTitle}>{copy.prefsErrorTitle}</strong>
        {copy.prefsErrorBlurb}
      </p>
    )
  }

  const mode = preferences?.automation_mode ?? ''
  const restart = preferences?.restart_required ?? null

  function chooseMode(next: string) {
    if (next === mode) {
      return
    }
    if (next === 'cautious') {
      setPendingMode(next)
      return
    }
    save({ automation_mode: next })
  }

  return (
    <div className={styles.page}>
      <header className={styles.head}>
        <h1 className={styles.title}>{copy.prefsTitle}</h1>
        <p className={styles.blurb}>{copy.prefsBlurb}</p>
      </header>

      {isLoading && <p className={styles.value}>{copy.prefsLoading}</p>}

      {!isLoading && (
        <div className={styles.grid}>
          {(preferences?.warnings ?? []).map((warning) => (
            <p key={warning} className={cx(styles.panel, styles.warning)} role="status">
              {copy.prefsWarning(warning)}
            </p>
          ))}

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsGeneral}</h2>
            <Row label={copy.prefsLanguage}>
              <Choice
                options={[
                  { value: 'zh', label: '中' },
                  { value: 'en', label: 'EN' },
                ]}
                current={language}
                onPick={(next) => {
                  setLanguage(next === 'en' ? 'en' : 'zh')
                  save({ language: next })
                }}
              />
            </Row>
            <Row label={copy.prefsTheme}>
              <Choice
                options={[
                  { value: 'dark', label: copy.prefsThemeDark },
                  { value: 'light', label: copy.prefsThemeLight },
                  { value: 'system', label: copy.prefsThemeSystem },
                ]}
                current={theme}
                onPick={(next) => {
                  setTheme(themeFrom(next))
                  save({ theme: next })
                }}
              />
            </Row>
            <Row label={copy.prefsStartup}>
              <span className={styles.value}>{copy.prefsStartupValue}</span>
            </Row>
            <Row label={copy.prefsIdleLock}>
              <span className={styles.value}>{copy.prefsIdleLockValue}</span>
            </Row>
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsGateway}</h2>
            <Row label={copy.prefsPort}>
              <span className={styles.mono}>{restart?.web_api_port ?? '—'}</span>
              <span className={styles.hint}>{copy.prefsRestartHint}</span>
            </Row>
            <Row label={copy.prefsListen}>
              <span className={styles.mono}>{restart?.listen_address ?? '—'}</span>
              <span className={styles.hint}>{copy.prefsRestartHint}</span>
            </Row>
            <Row label={copy.prefsProxyPort}>
              <span className={styles.mono}>{restart?.proxy_port ?? '—'}</span>
            </Row>
            <Row label={copy.prefsMcpPort}>
              <span className={styles.mono}>{restart?.mcp_port ?? '—'}</span>
            </Row>
            <Unavailable label={copy.prefsRemote} copy={copy} />
            <Unavailable label={copy.prefsTls} copy={copy} />
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsCredentials}</h2>
            <Row label={copy.prefsProviders}>
              <span className={styles.value}>{copy.prefsProvidersValue}</span>
            </Row>
            <Row label={copy.prefsVault}>
              <VaultSetup />
            </Row>
            <Row label={copy.prefsReauth}>
              <span className={styles.value}>{copy.prefsReauthValue}</span>
            </Row>
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsAutomation}</h2>
            <Row label={copy.prefsMode}>
              <div className={styles.modes}>
                {MODES.map((each) => (
                  <button
                    key={each}
                    type="button"
                    className={cx(styles.mode, each === mode && styles.modeOn)}
                    aria-pressed={each === mode}
                    onClick={() => {
                      chooseMode(each)
                    }}
                  >
                    <span className={styles.modeName}>{modeTextOf(each, copy)}</span>
                    <span className={styles.hint}>{modeHintOf(each, copy)}</span>
                  </button>
                ))}
              </div>
            </Row>
            {pendingMode !== '' && (
              <Confirm
                text={copy.prefsCautiousWarning}
                confirm={copy.prefsConfirm}
                cancel={copy.prefsCancel}
                onConfirm={() => {
                  save({ automation_mode: pendingMode })
                  setPendingMode('')
                }}
                onCancel={() => {
                  setPendingMode('')
                }}
              />
            )}
            <Row label={copy.prefsHighRisk}>
              <span className={styles.value}>{copy.prefsHighRiskValue}</span>
            </Row>
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsSecurity}</h2>
            <Row label={copy.prefsDeviceKey}>
              <span className={styles.value}>{copy.prefsDeviceKeyValue}</span>
            </Row>
            <Row label={copy.prefsBiometric}>
              <span className={styles.value}>{copy.prefsBiometricValue}</span>
            </Row>
            <Row label={copy.prefsRetention}>
              <span className={styles.value}>{copy.prefsRetentionValue}</span>
            </Row>
            <Row label={copy.prefsExport}>
              <span className={styles.value}>{copy.prefsExportValue}</span>
            </Row>
            <Row label={copy.prefsClear}>
              {memories.length === 0 ? (
                <span className={styles.value}>{copy.prefsClearEmpty}</span>
              ) : (
                <>
                  <span className={styles.value}>{copy.prefsClearHint(memories.length)}</span>
                  {/* 破坏性操作：二次确认（REQ-UI-007 AC3）。审计由后端在删除之前写下。 */}
                  {!confirmingClear && (
                    <button
                      type="button"
                      className={cx(styles.action, styles.danger)}
                      disabled={clearing}
                      onClick={() => {
                        setConfirmingClear(true)
                      }}
                    >
                      {clearing ? copy.prefsClearing : copy.prefsClear}
                    </button>
                  )}
                  {confirmingClear && (
                    <Confirm
                      text={copy.prefsClearHint(memories.length)}
                      confirm={copy.prefsClearConfirm}
                      cancel={copy.prefsCancel}
                      onConfirm={() => {
                        clear(memories.map((memory) => memory.id))
                        setConfirmingClear(false)
                      }}
                      onCancel={() => {
                        setConfirmingClear(false)
                      }}
                    />
                  )}
                </>
              )}
              {cleared > 0 && (
                <span className={styles.value} role="status">
                  {copy.prefsCleared(cleared)}
                </span>
              )}
              {clearFailed && <span className={styles.value}>{copy.prefsClearFailed}</span>}
            </Row>
          </section>

          <section className={styles.panel}>
            <h2 className={styles.eyebrow}>{copy.prefsNotifications}</h2>
            <Unavailable label={copy.prefsSystemNotice} copy={copy} />
            <Unavailable label={copy.prefsBrowserNotice} copy={copy} />
            <Unavailable label={copy.prefsMailNotice} copy={copy} />
          </section>
        </div>
      )}
    </div>
  )
}

function Row({ label, children }: { readonly label: string; readonly children: ReactNode }) {
  return (
    <div className={styles.row}>
      <span className={styles.label}>{label}</span>
      <span className={styles.control}>{children}</span>
    </div>
  )
}

/**
 * 本期没有的项。
 *
 * 画成禁用的行而不是干脆不画：用户需要知道「这件事还没有」，
 * 而不是以为自己漏看了什么（REQ-UI-007 AC2）。
 */
function Unavailable({ label, copy }: { readonly label: string; readonly copy: Copy }) {
  return (
    <div className={cx(styles.row, styles.rowOff)}>
      <span className={styles.label}>{label}</span>
      <span className={styles.control}>
        <button type="button" className={styles.action} disabled>
          {copy.prefsLater}
        </button>
        <span className={styles.hint}>{copy.prefsNotSupported}</span>
      </span>
    </div>
  )
}

interface ChoiceProps {
  readonly options: readonly { readonly value: string; readonly label: string }[]
  readonly current: string
  readonly onPick: (value: string) => void
}

function Choice({ options, current, onPick }: ChoiceProps) {
  return (
    <span className={styles.choice}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={cx(styles.chip, option.value === current && styles.chipOn)}
          aria-pressed={option.value === current}
          onClick={() => {
            onPick(option.value)
          }}
        >
          {option.label}
        </button>
      ))}
    </span>
  )
}

interface ConfirmProps {
  readonly text: string
  readonly confirm: string
  readonly cancel: string
  readonly onConfirm: () => void
  readonly onCancel: () => void
}

function Confirm({ text, confirm, cancel, onConfirm, onCancel }: ConfirmProps) {
  return (
    <div className={styles.confirm} role="alertdialog" aria-label={text}>
      <p className={styles.value}>{text}</p>
      <div className={styles.confirmActions}>
        <button type="button" className={cx(styles.action, styles.danger)} onClick={onConfirm}>
          {confirm}
        </button>
        <button type="button" className={styles.action} onClick={onCancel}>
          {cancel}
        </button>
      </div>
    </div>
  )
}

function modeTextOf(mode: string, copy: Copy): string {
  if (mode === 'cautious') {
    return copy.prefsModeCautious
  }
  if (mode === 'automatic') {
    return copy.prefsModeAutomatic
  }
  return copy.prefsModeBalanced
}

function modeHintOf(mode: string, copy: Copy): string {
  if (mode === 'cautious') {
    return copy.prefsModeCautiousHint
  }
  if (mode === 'automatic') {
    return copy.prefsModeAutomaticHint
  }
  return copy.prefsModeBalancedHint
}

/** 认不出的主题取值退回「跟随系统」：它不改变任何东西。 */
function themeFrom(raw: string): ThemePreference {
  return raw === 'dark' || raw === 'light' ? raw : 'system'
}
